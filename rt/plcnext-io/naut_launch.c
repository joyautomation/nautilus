// naut_launch: start the current nautilus release with real-time priority
// allowed, so that installing a new release does not need root.
//
// A task's `priority:` needs CAP_SYS_NICE. On a controller without root
// access (a PLCnext's admin) that capability comes from `setcap` on the
// binary, which only root can run and which every new binary loses. This
// program carries the capability instead, set once:
//
//   setcap cap_sys_nice+ep <dir>/naut-launch
//
// and passes it on: it raises CAP_SYS_NICE into its inheritable and
// ambient sets and execs <dir>/current/controller with its own arguments,
// keeping its pid. The release behind `current` can then change freely and
// keeps the capability; it gains nothing else.
//
// Checks, before anything is exec'd:
//   - the caller's real uid must own naut-launch, so no other user can run it;
//   - current/controller, resolved, must be a regular file owned by that
//     user, and <dir> and the controller's directory must belong to that
//     user (or root); none of the three may be writable by group or others.
//
// What that does not do: it does not confine the capability to nautilus.
// Anyone who can act as the owner (admin on a PLCnext) effectively holds
// CAP_SYS_NICE. They choose what `current` points at, every process the
// controller starts inherits the ambient capability, and an owner's
// process may ptrace the controller. CAP_SYS_NICE reaches every pid, the
// firmware's own FIFO tasks included, so misused it can starve the
// controller: the risk is availability, not confidentiality or more
// privilege. On a PLCnext, admin already has a documented way to root
// (`sudo passwd root`), so this hands admin nothing it could not take.
// Without the capability (setcap not run yet, or lost when this file was
// replaced) it says so on stderr and starts the controller anyway: nautilus
// then runs its tasks at normal priority, as it does on any box.
//
// No libcap: capget/capset and prctl are called directly.
#define _GNU_SOURCE
#include <errno.h>
#include <limits.h>
#include <stdint.h>
#include <linux/capability.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/resource.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <unistd.h>

#ifndef PR_CAP_AMBIENT
#define PR_CAP_AMBIENT 47
#endif
#ifndef PR_CAP_AMBIENT_RAISE
#define PR_CAP_AMBIENT_RAISE 2
#endif

#define TARGET "current/controller"

static void say(const char *msg, const char *arg) {
	fprintf(stderr, "naut-launch: %s%s%s\n", msg, arg ? ": " : "", arg ? arg : "");
}

// owned_safe: path is owned by uid (or root, when root_ok) and is not
// writable by group or others.
static int owned_safe(const char *path, uid_t uid, int want_dir, int root_ok) {
	struct stat st;
	if (stat(path, &st) != 0) {
		say(strerror(errno), path);
		return 0;
	}
	if (want_dir ? !S_ISDIR(st.st_mode) : !S_ISREG(st.st_mode)) {
		say(want_dir ? "not a directory" : "not a regular file", path);
		return 0;
	}
	if (st.st_uid != uid && !(root_ok && st.st_uid == 0)) {
		say("owned by another user", path);
		return 0;
	}
	if (st.st_mode & (S_IWGRP | S_IWOTH)) {
		say("writable by group or others", path);
		return 0;
	}
	return 1;
}

// pass_sys_nice raises CAP_SYS_NICE into the inheritable and ambient sets,
// so the program exec'd next has it in its permitted and effective sets.
// It returns 0 when there was nothing to pass on.
static int pass_sys_nice(void) {
	struct __user_cap_header_struct hdr = {_LINUX_CAPABILITY_VERSION_3, 0};
	struct __user_cap_data_struct data[2];
	memset(data, 0, sizeof data);
	if (syscall(SYS_capget, &hdr, data) != 0) {
		say("capget", strerror(errno));
		return 0;
	}
	const unsigned bit = 1u << (CAP_SYS_NICE % 32);
	const int word = CAP_SYS_NICE / 32;
	if (!(data[word].permitted & bit)) {
		say("no cap_sys_nice on naut-launch (setcap cap_sys_nice+ep it once); "
		    "starting the controller without real-time priority", NULL);
		return 0;
	}
	data[word].inheritable |= bit;
	if (syscall(SYS_capset, &hdr, data) != 0) {
		say("capset", strerror(errno));
		return 0;
	}
	if (prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_RAISE, CAP_SYS_NICE, 0, 0) != 0) {
		say("ambient raise", strerror(errno));
		return 0;
	}
	return 1;
}

#ifndef RLIMIT_RTTIME
#define RLIMIT_RTTIME 15
#endif

// bound_rt sets RLIMIT_RTTIME before the exec, as the nautilus runtime does
// before a task gets SCHED_FIFO (#836): a real-time thread of anything this
// starts may run 200 ms of CPU without blocking (NAUTILUS_RT_LIMIT_MS, 10 to
// 60000, changes it), then gets SIGXCPU; at 2.5 times that, SIGKILL. The
// cap and the 64-bit arithmetic matter on the controller: armv7's long and
// rlim_t are 32 bits, and a large value would wrap into a tiny limit. Limits
// survive exec, so this bounds programs that set no limit of their own
// (tools/jitter, a C program); a stricter limit already in force is kept,
// and naut's own is the same or stricter. A failure is said, not fatal: the
// launcher's job is to start the controller.
static void bound_rt(void) {
	uint64_t ms = 200;
	const char *v = getenv("NAUTILUS_RT_LIMIT_MS");
	if (v && *v) {
		char *end;
		errno = 0;
		long n = strtol(v, &end, 10);
		if (errno == 0 && *end == '\0' && n >= 10 && n <= 60000) {
			ms = (uint64_t)n;
		} else {
			say("NAUTILUS_RT_LIMIT_MS is not a whole number of milliseconds from 10 to 60000; using 200", v);
		}
	}
	// At most 150 000 000 µs: fits a 32-bit rlim_t.
	struct rlimit cur, want = {(rlim_t)(ms * 1000), (rlim_t)(ms * 2500)};
	if (getrlimit(RLIMIT_RTTIME, &cur) == 0) {
		if (cur.rlim_cur < want.rlim_cur) want.rlim_cur = cur.rlim_cur;
		if (cur.rlim_max < want.rlim_max) want.rlim_max = cur.rlim_max;
		if (want.rlim_cur > want.rlim_max) want.rlim_cur = want.rlim_max;
	}
	if (setrlimit(RLIMIT_RTTIME, &want) != 0) {
		say("RLIMIT_RTTIME not set; a runaway real-time thread is bounded only by the controller's own guards", strerror(errno));
	}
}

int main(int argc, char **argv) {
	// execve allows an empty argv; then argv[0] is the NULL terminator and
	// overwriting it below would turn the environment into arguments.
	if (argc < 1) {
		say("started with no arguments at all, not even its name", NULL);
		return 127;
	}
	char self[PATH_MAX], dir[PATH_MAX], target[PATH_MAX], real[PATH_MAX];
	if (!realpath("/proc/self/exe", self)) {
		say("cannot find itself", strerror(errno));
		return 127;
	}
	struct stat me;
	if (stat(self, &me) != 0) {
		say(strerror(errno), self);
		return 127;
	}
	if (getuid() != me.st_uid) {
		say("only the user who owns naut-launch may run it", self);
		return 126;
	}
	snprintf(dir, sizeof dir, "%s", self);
	char *slash = strrchr(dir, '/');
	if (!slash) {
		say("odd path", self);
		return 127;
	}
	*slash = '\0';
	if ((size_t)snprintf(target, sizeof target, "%s/%s", dir, TARGET) >= sizeof target) {
		say("path too long", dir);
		return 127;
	}
	if (!realpath(target, real)) {
		say(strerror(errno), target);
		return 127;
	}
	char realdir[PATH_MAX];
	snprintf(realdir, sizeof realdir, "%s", real);
	*strrchr(realdir, '/') = '\0';
	if (!owned_safe(dir, me.st_uid, 1, 1) || !owned_safe(realdir, me.st_uid, 1, 1) ||
	    !owned_safe(real, me.st_uid, 0, 0)) {
		return 126;
	}
	// Defence in depth: the controller is a static Go binary and glibc
	// already drops these for a file-capability program like this one, but
	// nothing from the caller's environment should steer a loader that runs
	// with the capability.
	unsetenv("LD_PRELOAD");
	unsetenv("LD_LIBRARY_PATH");
	unsetenv("LD_AUDIT");
	bound_rt();
	pass_sys_nice();
	argv[0] = real;
	execv(real, argv);
	say(strerror(errno), real);
	return 127;
}
