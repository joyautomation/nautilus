# errors.joyautomation.com: GlitchTip

Self-hosted error monitoring (GlitchTip, Sentry protocol) for the docs site and
the browser playground (#1073). It runs in the `joy` namespace of the
DigitalOcean cluster `k8s-joy-1`, beside the docs site.

**Nothing here is applied by CI.** `docs.yml` deploys only the docs site and the
playground's share service. Merging this folder changes nothing live; the apply
below is a separate step, run by hand after James says go.

| File | What |
|---|---|
| `glitchtip.yml` | GlitchTip `v7.0.0` (Deployment, Service, Ingress with TLS) |
| `postgres.yml` | Postgres 17 StatefulSet, 10 Gi `do-block-storage-retain` volume |
| `redis.yml` | Redis 7.4 (task queue and cache, password, no volume) |

Since v7 GlitchTip is one process (web, task worker and scheduler together), so
there is no separate worker Deployment. `SERVER_ROLE=worker` is refused by its
own start script. Resource requests are small (about 160 m CPU and 416 Mi in
all); the limits cap GlitchTip at 512 Mi, the size its docs recommend.

No managed database: the cluster has none for this, and an in-cluster Postgres
matches how the share service keeps its data. (The cluster also runs
CloudNativePG; moving to a CNPG `Cluster` later only changes `DATABASE_URL`.)

## Secrets (once, out of band)

Three random values, hex so they are safe inside a URL, are kept in Bitwarden as
items in the vault (names are what the command below reads):

- `errors-glitchtip secret-key`
- `errors-glitchtip postgres`
- `errors-glitchtip redis`

Make each with `openssl rand -hex 32` and paste it into the item's password
field in the Bitwarden app; the values never touch a file or a terminal's
output. If the vault is locked, run `bwu` in your own terminal first. Do not run
`bw unlock`, `bw lock` or `bw logout` from a script or an agent session.

Create the Secret. The values go through `bw` and a pipe into stdin of
`kubectl`; none is printed or in any argv (`printf` is a shell builtin):

```sh
export BW_SESSION=$(cat ~/.config/bw-session)
bw sync >/dev/null
{
  printf 'SECRET_KEY=%s\n'        "$(bw get password 'errors-glitchtip secret-key')"
  printf 'POSTGRES_PASSWORD=%s\n' "$(bw get password 'errors-glitchtip postgres')"
  printf 'REDIS_PASSWORD=%s\n'    "$(bw get password 'errors-glitchtip redis')"
} | kubectl -n joy create secret generic glitchtip --from-env-file=/dev/stdin
```

`kubectl -n joy get secret glitchtip -o jsonpath='{.data}' | jq 'keys'` shows
the three key names without their values.

**Rotating** a value: change the Bitwarden item, then repeat the command with
`kubectl ... create secret ... --dry-run=client -o yaml | kubectl apply -f -`
in place of the plain `create`, and restart the pods. Rotating
`POSTGRES_PASSWORD` also needs `ALTER USER glitchtip PASSWORD ...` inside
Postgres (it is only read when the database is first created).

### Email

None yet: `EMAIL_URL` is `consolemail://` in `glitchtip.yml`, so GlitchTip's
mail (invitations, alerts) lands in its pod log. To send real mail, add an
`EMAIL_URL` key (`smtp://user:password@host:port`, with the same stdin recipe and
a Bitwarden item) to the Secret, and change the `EMAIL_URL` entry in the
Deployment to a `valueFrom.secretKeyRef`. Until then, read invitation links in
`kubectl -n joy logs deploy/glitchtip`.

## DNS

All joyautomation.com DNS is DigitalOcean DNS (`ns1-3.digitalocean.com`).
`nautilus.joyautomation.com` is an `A` record to the cluster's ingress load
balancer, and `errors` is the same: point it at the same address, which is read
from the existing record rather than written down here:

```sh
doctl compute domain records create joyautomation.com \
  --record-type A --record-name errors --record-ttl 300 \
  --record-data "$(dig +short nautilus.joyautomation.com | head -1)"
```

cert-manager then issues the certificate (`letsencrypt-prod`, HTTP-01) once the
name resolves: `kubectl -n joy get certificate glitchtip-tls` turns `READY`
within a few minutes. The ingress and the issuer are the docs site's;
`website/k8s/deploy.yml` notes they were copied from common DO setups, so before
the first apply check them against the cluster: `kubectl get ingress -n joy -o yaml`
(class `nginx`, annotation `cert-manager.io/cluster-issuer: letsencrypt-prod`)
and `kubectl get clusterissuer`.

## Apply (James's go only)

```sh
doctl kubernetes cluster kubeconfig save --expiry-seconds 600 k8s-joy-1
kubectl apply -f infra/errors/postgres.yml -f infra/errors/redis.yml
kubectl -n joy rollout status statefulset/glitchtip-postgres --timeout=5m
kubectl apply -f infra/errors/glitchtip.yml
kubectl -n joy rollout status deployment/glitchtip --timeout=5m
```

The Secret must exist first (above). The first start runs the database setup
(`init_database`), so it takes a minute; `kubectl -n joy logs deploy/glitchtip`.

## First user, organisation, DSN

Self-signup is off (`ENABLE_USER_REGISTRATION=false`), but GlitchTip still lets
the very first user register, so create that user right after the first start,
before the DNS name is public if you can:

```sh
kubectl -n joy exec -it deploy/glitchtip -- ./manage.py createsuperuser
```

It asks for an email and a password on the terminal (the password never goes in
argv). Then:

1. Sign in at <https://errors.joyautomation.com/>.
2. Create the organisation (a superuser can; `ENABLE_ORGANIZATION_CREATION` stays
   off for everyone else).
3. Create a project, platform **JavaScript** (the browser SDK; the playground
   and docs both use it). One project for the docs site and playground together
   is enough to start; split them later if their noise differs.
4. **Project settings → Client Keys (DSN)** shows the DSN:
   `https://<public-key>@errors.joyautomation.com/<project-id>`. That is the
   value #1073's client reads at docs-build time (no DSN set: nothing is sent).
   A browser DSN is public by design: it can only send events, not read them. It
   still belongs in the build's configuration (a repository variable), not in a
   committed file, so it can be rotated.
5. Optional: Project settings → set a rate limit, and a data-retention period
   under the organisation, so a noisy page cannot fill the 10 Gi volume.

What the client sends and scrubs is #1073's: stack traces and the build id,
never project files, code or tag values.

## Upgrading

Change the image tag in `glitchtip.yml` (check the
[GlitchTip blog](https://glitchtip.com/blog) first for a major version: they
can drop old PostgreSQL versions) and apply it. Database migrations run on
start. Postgres: change the minor tag in `postgres.yml`; a major version needs
a dump and restore.

## Roll back

A bad GlitchTip release: `kubectl -n joy rollout undo deployment/glitchtip`, or
put the old tag back and apply. Migrations are not undone by that, so after a
version with migrations, restore the database first (below) before going back
more than a patch version.

Backup before an upgrade (to a file on your machine, never into git):

```sh
kubectl -n joy exec glitchtip-postgres-0 -- pg_dump -U glitchtip -Fc glitchtip > glitchtip-$(date +%F).dump
```

Restore into an empty database:
`kubectl -n joy exec -i glitchtip-postgres-0 -- pg_restore -U glitchtip -d glitchtip --clean --if-exists < glitchtip-DATE.dump`.

## Delete everything

In this order; the volume's class is `Retain`, so the data survives the first
two steps on purpose.

```sh
# 1. Stop serving. Events sent meanwhile are lost (the SDK drops them).
kubectl -n joy delete -f infra/errors/glitchtip.yml
kubectl -n joy delete -f infra/errors/redis.yml
# 2. Back up if you want the history (above), then remove Postgres.
kubectl -n joy delete -f infra/errors/postgres.yml
# 3. The data: the claim (kept by the StatefulSet), then the retained volume.
kubectl -n joy delete pvc data-glitchtip-postgres-0
kubectl get pv | grep glitchtip     # note the volume's name
kubectl delete pv <that-name>       # and in DigitalOcean: Volumes → delete it
# 4. The Secret, the certificate secret and the DNS record.
kubectl -n joy delete secret glitchtip glitchtip-tls
doctl compute domain records list joyautomation.com | grep errors   # the id
doctl compute domain records delete joyautomation.com <id>
```

Then remove the three Bitwarden items. If the docs build still has the DSN set,
unset it too, so the client stops sending to a name that no longer resolves.
