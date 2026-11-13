# Tutorial: your first controller

This tutorial takes you from nothing installed to a motor start/stop
program running on your laptop, with a live screen in your browser. It
takes about an hour. You do not need any hardware.

You will:

1. Install VS Code, the nautilus extension and the `naut` command.
2. Create a project.
3. Write a motor start/stop seal-in in ladder.
4. Test it.
5. Run it and use the built-in dashboard in your browser.
6. Watch and force tags live in VS Code.
7. Add a small Structured Text program.
8. Build a simple operator screen (HMI).
9. Build one file you can copy to a controller.

Each step is one action. Each says what you should see. If you do not see
it, go to [When something goes wrong](#when-something-goes-wrong) at the end.

You need a computer running macOS, Windows or Linux and an internet
connection.

Words used here:

- **Controller**: the program that runs your logic in a loop. nautilus
  runs it on your laptop.
- **Scan**: one pass of that loop. Here, one scan takes 100 ms.
- **Tag**: a named value, such as `MotorRun`.
- **Terminal**: the window where you type commands. On macOS it is
  *Terminal*. On Windows it is *PowerShell*. VS Code has one built in.

## Part 1. Install

### 1.1 Install VS Code

1. Go to <https://code.visualstudio.com/> and download VS Code for your
   system.
2. Install it and open it.

You should see the VS Code welcome page.

### 1.2 Install the nautilus extension

1. In VS Code, click the *Extensions* icon on the left (four squares), or
   press `Ctrl+Shift+X` (macOS: `Cmd+Shift+X`).
2. In the search box, type `nautilus IEC 61131-3`.
3. Click *Install* on the result named *nautilus IEC 61131-3* by `joyauto`.
4. A window asks *Do you trust the publisher "joyauto"?* Click
   *Trust Publisher & Install*.
5. Click the result's name to open its page.

You should see the extension's page with an *Uninstall* button.

### 1.3 Install the `naut` command

`naut` is the nautilus tool. It creates, tests and runs projects. The
extension can install its own copy, but that copy is only for VS Code. You
need `naut` in a terminal too, so install it as follows.

1. Open VS Code's terminal: menu *Terminal*, then *New Terminal*.
2. Find your system below and paste the commands.

**macOS** (also works on Linux). Paste this whole block, then press Enter:

```sh
v=$(curl -fsSL https://api.github.com/repos/joyautomation/nautilus/releases/latest | grep '"tag_name"' | cut -d'"' -f4)
os=$(uname -s | tr A-Z a-z)
arch=$(uname -m)
[ "$arch" = x86_64 ] && arch=amd64
[ "$arch" = aarch64 ] && arch=arm64
curl -fsSLO "https://github.com/joyautomation/nautilus/releases/download/$v/nautilus_${v#v}_${os}_${arch}.tar.gz"
tar xzf "nautilus_${v#v}_${os}_${arch}.tar.gz" naut
sudo install -d /usr/local/bin
sudo install naut /usr/local/bin/naut
```

The last two lines ask for your computer password. Nothing appears while
you type it. That is normal. Type it and press Enter.

**Windows** (PowerShell). Paste this whole block, then press Enter:

```powershell
$v = (Invoke-RestMethod https://api.github.com/repos/joyautomation/nautilus/releases/latest).tag_name
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
Invoke-WebRequest "https://github.com/joyautomation/nautilus/releases/download/$v/nautilus_$($v.TrimStart('v'))_windows_$arch.zip" -OutFile nautilus.zip
Expand-Archive nautilus.zip -DestinationPath "$env:LOCALAPPDATA\nautilus" -Force
[Environment]::SetEnvironmentVariable("Path", "$env:LOCALAPPDATA\nautilus;" + [Environment]::GetEnvironmentVariable("Path", "User"), "User")
```

Then close the terminal and open a new one. Windows only reads the new
`Path` in a new terminal.

3. Type this and press Enter:

```sh
naut version
```

You should see a line like `nautilus 0.19.0`. If you see "command not
found" or "not recognized", see
[naut: command not found](#naut-command-not-found).

The same archives are on the
[release page](https://github.com/joyautomation/nautilus/releases/latest)
if you would rather download by hand. The command is `naut`, not
`nautilus`, because many Linux desktops already have a file manager with
that name.

## Part 2. Create a project

1. In the terminal, go to a folder for your work. For example:

```sh
cd ~/Documents
```

2. Create the project:

```sh
naut new motor --template minimal --language ld
```

You should see `created motor/ — a manifest project, no Go required`
and a short list of next steps.

3. In VS Code, choose *File*, then *Open Folder…* (macOS: *File*, then
   *Open…*). In the *Open Folder* window, pick the `motor` folder in
   `Documents` and click *Open*.
4. If VS Code asks *Do you trust the authors of the files in this
   folder?*, click *Yes, I trust the authors*. You made the folder a
   moment ago.
5. If VS Code offers to install recommended extensions, click *Install*.

You should see these files in the Explorer on the left, among a few
others (`README.md`, `AGENTS.md`, `CLAUDE.md`, `.gitignore` and the
`.github` and `.vscode` folders):

- `nautilus.yaml`: the project. It lists the tags and the programs.
- `program.ld`: your ladder program.
- `motor_test.yaml`: tests for your program.

## Part 3. Write the motor start/stop

A motor start/stop uses a **seal-in**. The Start button turns the motor
on. The motor's own output then holds itself on, through a contact in
parallel with Start. The Stop button breaks the rung and the motor drops
out.

### 3.1 Declare the tags

1. Click `nautilus.yaml` in the Explorer.
2. Select everything (`Ctrl+A`, macOS `Cmd+A`) and replace it with:

```yaml
name: motor

server:
  addr: "localhost:8080"
  online-edits: true

tasks:
  - program: program.ld
    scan: 100ms

tags:
  - { name: StartPB,  role: setpoint, type: BOOL, init: false, desc: Start button (press = TRUE) }
  - { name: StopPB,   role: setpoint, type: BOOL, init: false, desc: Stop button (press = TRUE) }
  - { name: MotorRun, role: output,   type: BOOL, init: false, desc: Motor contactor }

driver:
  type: memory
```

3. Save (`Ctrl+S`, macOS `Cmd+S`).

What this says:

- There is one program, `program.ld`. It runs every 100 ms.
- `StartPB` and `StopPB` are the two buttons. They are `setpoint` tags, so
  you can click them from a dashboard. A real button would be an `input`
  tag wired to a field device.
- `MotorRun` is an `output`. The program writes it.
- `driver: memory` means there is no hardware. The tags live in memory.

### 3.2 Write the rung

1. Click `program.ld`.
2. Replace everything with:

```
PROGRAM Motor
LD
  RUNG seal_in (* Start or already running, and Stop not pressed *)
    [ StartPB | MotorRun ] /StopPB ( MotorRun )
END_LD
END_PROGRAM
```

3. Save.

Read the rung from left to right:

- `[ StartPB | MotorRun ]` is two contacts in parallel. Power passes if
  `StartPB` is TRUE **or** `MotorRun` is TRUE.
- `/StopPB` is a normally closed contact. Power passes if `StopPB` is
  FALSE.
- `( MotorRun )` is the coil. It turns `MotorRun` on when power reaches it.

### 3.3 See it as a ladder diagram

1. Right-click the `program.ld` tab at the top.
2. Choose *Reopen Editor With…*, then, in the list that appears, *Ladder
   Diagram*.

You should see the rung drawn with contacts and a coil, and a yellow
*offline* pill at the top right (it turns green when a controller is
running, in Part 5). To see the text, click *Show Source* in the editor's
title bar (the icon with a file and an arrow). The text opens beside the
diagram.

### 3.4 Check it

1. In the terminal, make sure you are in the `motor` folder. VS Code's
   *Terminal*, then *New Terminal* opens there.
2. Type:

```sh
naut check .
```

You should see `naut check: 1 file(s), 0 with errors`.

To see an error, click *Show Source* to open the text, change `StartPB` in
the rung to `StartPBX`, save, and look at the *Problems* panel (menu
*View*, then *Problems*). You should see `undeclared identifier
"StartPBX"`. Change it back and save.

## Part 4. Test it

Acceptance tests describe what the program must do. They run on a virtual
clock, so they finish in milliseconds.

1. Click `motor_test.yaml`.
2. Replace everything with:

```yaml
tests:
  - name: start button starts the motor and it stays on
    steps:
      - given: { StartPB: true }
        scans: 1
        expect: { MotorRun: true }
      - given: { StartPB: false }
        scans: 1
        expect: { MotorRun: true }    # sealed in

  - name: stop button drops the motor out
    steps:
      - given: { StartPB: true }
        scans: 1
      - given: { StartPB: false }
        scans: 1
      - given: { StopPB: true }
        scans: 1
        expect: { MotorRun: false }
      - given: { StopPB: false }
        scans: 1
        expect: { MotorRun: false }   # stays off until Start
```

3. Save.
4. In the terminal, type:

```sh
naut test -v .
```

You should see two lines starting with `ok`, then `2 tests, 2 passed, 0 failed`.

Each step says: set these tags (`given`), run this many scans (`scans`),
then check these tags (`expect`).

To see a failure, change `MotorRun: true` on the first `expect` to
`MotorRun: false`, save and run `naut test .` again. You should see a
failed test that says what it expected and what it got. Change it back.

## Part 5. Run it and open the dashboard

1. In the terminal, type:

```sh
naut run
```

You should see:

```
nautilus · motor — dashboard + tag API on http://localhost:8080 — Ctrl+C to stop
```

The terminal stays busy. That is correct. The controller is running. Leave
it running for the rest of Parts 5 to 7.

2. Open a web browser and go to <http://localhost:8080>.

You should see the nautilus dashboard. Scroll to *Live tags*. You should
see `StartPB`, `StopPB`, `MotorRun` and the values `false`.

3. In the *Live tags* table, click the toggle next to `StartPB`.

You should see `StartPB` turn on and `MotorRun` turn on.

4. Click the `StartPB` toggle again to turn it off.

You should see `StartPB` off and `MotorRun` **still on**. That is the
seal-in.

5. Click the `StopPB` toggle on.

You should see `MotorRun` turn off.

6. Click the `StopPB` toggle off. `MotorRun` stays off.

To stop the controller, click in the terminal and press `Ctrl+C`.

## Part 6. Watch and force tags in VS Code

Keep `naut run` going. If you stopped it, start it again.

### 6.1 Watch values live

1. In VS Code, open `program.ld` as a ladder diagram (Part 3.3).

You should see the rung with live values and power flow. Wires that carry
power are highlighted. The status bar at the bottom right says *nautilus:
live* and the diagram has a green *live* pill. The project already points
at `http://localhost:8080`, so VS Code connects by itself.

2. If you see *offline* instead, press `Ctrl+Shift+P` (macOS
   `Cmd+Shift+P`), type `nautilus: Connect to Controller`, and press Enter
   to accept `http://localhost:8080`. A message says *nautilus: connected*.
3. In the browser dashboard, click the `StartPB` toggle on and off.

You should see the rung light up in VS Code. After `StartPB` goes off, the
`MotorRun` contact keeps the rung lit.

### 6.2 Open the Live Values panel

1. Click the *nautilus* icon in the Activity Bar on the left (three
   horizontal lines).

You should see *NAUTILUS: LIVE VALUES* with a *Tags* group that lists every
tag and its value.

### 6.3 Set a value

The motor is running after step 6.1. Stop it from VS Code.

1. In the *Live Values* list, point at `StopPB`. A lock and a pencil
   appear at the right of the row. Click the pencil.
2. Type `true` and press Enter.

You should see a message *nautilus: set StopPB = TRUE* and `MotorRun` go
off.

3. Click the pencil next to `StopPB` again, type `false`, and press Enter.

A normal write happens once. The next write, from the dashboard or from
the logic, replaces it.

### 6.4 Force a value

A **force** holds a tag at a value until you remove it. Technicians use
forces to test an interlock before the real sensor is wired.

1. In the dashboard, click the `StartPB` toggle on, then off. The motor
   runs again.
2. In VS Code, open `program.ld` as text: click *Show Source* in the
   diagram's title bar. If the text is too narrow to read, close the
   diagram on the left.
3. Right-click `StopPB` in the rung.
4. Choose *nautilus: Force…*, type `true`, and press Enter. A window asks
   *Force StopPB (now FALSE) to TRUE on http://localhost:8080?* Click
   *Force*.

You should see `MotorRun` go off, an amber *F TRUE* next to `StopPB`, a
*Forces* group in the *Live Values* list, and *1 force active* in the
status bar at the bottom. The force holds `StopPB` at `true`, so the rung
cannot pass power.

5. In the dashboard, click the `StartPB` toggle on.

You should see `StartPB` change but `MotorRun` stay off. Now try the
`StopPB` toggle. The controller refuses the write, because the tag is
forced.

6. Click *1 force active* in the status bar. In the list, choose *Remove
   all forces*, then click *Remove All* in the window that asks.

You should see the *F TRUE* disappear. `StartPB` is still on, so the motor
starts.

7. In the dashboard, click the `StartPB` toggle off, then click the
   `StopPB` toggle on and off. The motor ends up stopped.

## Part 7. Add a Structured Text program

Structured Text (ST) is the text language of IEC 61131-3. You will add a
second program that counts how many times the motor started. Ladder and ST
can run side by side in one project.

### 7.1 Add the program

1. In VS Code, choose *File*, then *New Text File*. Paste:

```
PROGRAM Stats
VAR
    started : R_TRIG;  (* TRUE for one scan when the motor starts *)
    starts  : CTU;     (* counts those scans *)
END_VAR

started(CLK := MotorRun);
starts(CU := started.Q, R := FALSE, PV := 32767);
MotorStarts := starts.CV;
END_PROGRAM
```

2. Save it as `stats.st` in the `motor` folder (`Ctrl+S`, macOS `Cmd+S`).

`R_TRIG` is a rising-edge detector. It is TRUE for one scan when `MotorRun`
goes from off to on. `CTU` is an up-counter. It counts those pulses.

### 7.2 Tell the project about it

1. Click `nautilus.yaml`.
2. Under `tasks:`, add a second program, so it reads:

```yaml
tasks:
  - program: program.ld
    scan: 100ms
  - program: stats.st
    scan: 100ms
```

3. Under `tags:`, add one more line after `MotorRun`:

```yaml
  - { name: MotorStarts, role: output, type: INT, init: 0, desc: Starts since power-up }
```

4. Save.

### 7.3 Add a test

1. Click `motor_test.yaml`.
2. Add this at the end of the file:

```yaml

  - name: every start is counted
    steps:
      - given: { StartPB: true }
        scans: 3
        expect: { MotorStarts: 1 }
      - given: { StartPB: false, StopPB: true }
        scans: 3
        expect: { MotorStarts: 1 }
      - given: { StartPB: true, StopPB: false }
        scans: 3
        expect: { MotorStarts: 2 }
```

3. Save. Stop the controller if it is running (`Ctrl+C` in its terminal).
4. Run the tests:

```sh
naut test .
```

You should see `3 tests, 3 passed, 0 failed`.

### 7.4 Run it again

1. Type `naut run`.
2. Reload <http://localhost:8080>.
3. Toggle `StartPB` on, off, then `StopPB` on, off, and `StartPB` on, off.

You should see `MotorStarts` count to 2. Press `Ctrl+C` in the terminal
when you are done.

To see the tags next to your ST code, open `stats.st` while the controller
is running. Each name shows its live value.

## Part 8. Build a simple operator screen

The dashboard is for engineers. An operator screen (HMI) shows what the
operator needs and nothing else. nautilus HMIs are small web apps, written
in Svelte, and `naut` builds them itself: there is nothing else to install.
You will make one with a motor symbol, a status line, and Start and Stop
buttons.

### 8.1 Create the page

1. Make a folder named `hmi` inside `motor`. In VS Code, right-click an
   empty space in the Explorer, choose *New Folder…*, and type `hmi`.
2. Inside it, make the folders `src`, then `src/routes`, the same way.
3. In `hmi/src/routes`, create a file named `+page.svelte` (right-click the
   folder, *New File…*) with this content:

```svelte
<script>
	import '@joyautomation/nautilus-hmi/theme.css';
	import { onMount } from 'svelte';
	import { Pump, StatusPill, CommandButton, createRealtimeClient } from '@joyautomation/nautilus-hmi';

	const rt = createRealtimeClient({ url: '/api/stream' });
	onMount(() => {
		rt.start();
		return () => rt.stop();
	});

	let tags = $derived(rt.frame?.tags ?? {});
	const write = (name, value) => rt.writeTag(name, value);
</script>

<main>
	<h1>Motor M-101</h1>
	<StatusPill kind={rt.connected ? 'good' : 'off'} label={rt.connected ? 'live' : 'no connection'} />

	<Pump running={tags.MotorRun === true} speedPct={100} label="M-101" />
	<p>{tags.MotorRun ? 'Running' : 'Stopped'} · starts: {tags.MotorStarts ?? 0}</p>

	<div class="buttons">
		<CommandButton tag="StartPB" label="Start" kind="start" {write} />
		<CommandButton tag="StopPB" label="Stop" kind="stop" {write} />
	</div>
</main>

<style>
	main {
		max-width: 24rem;
		margin: 2rem auto;
		font-family: system-ui, sans-serif;
		display: grid;
		gap: 1rem;
		justify-items: start;
	}
	.buttons {
		display: flex;
		gap: 0.75rem;
	}
</style>
```

What the page does: it opens a live connection to the controller
(`createRealtimeClient`), reads `MotorRun` and `MotorStarts` from it, draws
a pump symbol, and writes `StartPB` and `StopPB` when you press the
buttons. `CommandButton` presses a button for 0.4 s, like a real push
button.

The folder is laid out the way SvelteKit lays out an app: each page is a
`+page.svelte` under `src/routes` (`src/routes/pumps/+page.svelte` is the
page at `/pumps`), shared components go in `src/lib`, and files in
`static` are served as they are.

### 8.2 Tell the controller to serve it

1. Click `nautilus.yaml`.
2. Under `server:`, add a line so it reads:

```yaml
server:
  addr: "localhost:8080"
  online-edits: true
  hmi: hmi
```

3. Save.

### 8.3 Run it

1. Type:

```sh
naut run
```

You should see `dashboard` replaced by `hmi (hmi/, 1 page, built in …)` in
the first line: `naut` built the screen before it started.

2. Open <http://localhost:8080>.

You should see *Motor M-101*, a green *live* pill, a pump symbol, the text
*Stopped · starts: 0*, and two buttons, *Start* and *Stop*.

3. Click *Start*.

You should see the pump turn blue and the text change to
*Running · starts: 1*.

4. Click *Stop*.

You should see the pump stop and the text change to *Stopped*.

The engineering dashboard has moved to <http://localhost:8080/_nautilus/>.

### 8.4 Change the screen

1. In `hmi/src/routes/+page.svelte`, change `Motor M-101` in the `<h1>` to
   another name and save.
2. Stop `naut run` (`Ctrl+C`) and start it again, then reload the page.

You should see the new name.

To check a screen without running it, type `naut hmi build`. It builds the
screen the same way and lists anything wrong with it, each with its file
and line.

The screen can import Svelte, the HMI kit
(`@joyautomation/nautilus-hmi`), the 3D kit
(`@joyautomation/nautilus-hmi-3d`), three and Threlte: `naut` carries
those. The same `hmi` folder also builds with SvelteKit and npm, if you add
its `package.json` and config, for a package `naut` doesn't carry.

## Part 9. Build one file to ship

1. Stop the controller (`Ctrl+C`).
2. In the project folder, type:

```sh
naut build .
```

You should see `built motor — a self-contained controller`. The file
`motor` (on Windows, `motor.exe`) is the whole controller, with your logic
and your screen inside. Copy it to any computer with the same operating
system and run it.

## Where to go next

- [Run the examples](../examples/README.md#run-an-example-and-see-its-hmi):
  real plant projects with a full HMI, and a 3D view.
- [Your first program](https://nautilus.joyautomation.com/guides/first-program/):
  a sump pump with a timer, written in Structured Text.
- [Ladder](https://nautilus.joyautomation.com/languages/ladder/),
  [Structured Text](https://nautilus.joyautomation.com/languages/structured-text/),
  [Function Block](https://nautilus.joyautomation.com/languages/function-block/)
  and [SFC](https://nautilus.joyautomation.com/languages/sfc/) reference.
- [Forcing](https://nautilus.joyautomation.com/guides/forcing/) and
  [Testing](https://nautilus.joyautomation.com/reference/testing/).
- [The HMI kit](https://nautilus.joyautomation.com/reference/hmi/): every
  component you can put on a screen.

## When something goes wrong

### naut: command not found

- **Windows:** you did not open a new terminal after installing. Close
  every terminal and open a new one. If it still fails, run
  `$env:LOCALAPPDATA\nautilus\naut.exe version`. If that works, the file is
  there and the `Path` setting did not stick. Run the last line of the
  Windows install block again, then open a new terminal.
- **macOS:** the install block needs your computer password. If you
  cancelled it, run the block again. If you do not have administrator
  rights, install into your home folder instead:

```sh
mkdir -p ~/bin && mv naut ~/bin/ && echo 'export PATH="$HOME/bin:$PATH"' >> ~/.zshrc
```

  Then open a new terminal.
- **Any system:** `naut` has to be in the same terminal you are typing in.
  VS Code's own terminal (*Terminal*, *New Terminal*) is the safest
  choice.

### macOS says "naut cannot be opened" or "Apple could not verify"

The `naut` binary is not signed with an Apple developer certificate.
Downloading it with the `curl` commands above avoids the warning. If you
downloaded it with a browser and macOS blocks it, remove the quarantine
flag:

```sh
xattr -d com.apple.quarantine /usr/local/bin/naut
```

Or open *System Settings*, then *Privacy & Security*, scroll to the
message about `naut`, and click *Allow Anyway*. Then run `naut version`
again and click *Open* when macOS asks.

### Windows says "Windows protected your PC" or the file is blocked

`naut.exe` is not signed with a Windows publisher certificate. If you
downloaded the zip in a browser, Windows marks it as from the internet.
Either click *More info*, then *Run anyway*, or unblock it once:

```powershell
Unblock-File "$env:LOCALAPPDATA\nautilus\naut.exe"
```

The PowerShell block in Part 1 downloads without that mark, so it does not
normally appear.

### Windows Defender Firewall asks about `naut`

The controller listens only on `localhost`, so you can click *Cancel* or
allow *Private networks* only.

### VS Code says it cannot start the language server

The extension looks for `naut` itself. On macOS, VS Code started from the
Dock does not see the same `PATH` as your terminal. Fix it one of these
ways:

- Press `Ctrl+Shift+P` (macOS `Cmd+Shift+P`) and run
  `nautilus: Install or Update the naut CLI`. The extension keeps its own
  copy.
- Or set the full path. Run `which naut` (Windows: `where.exe naut`) in a
  terminal. Open VS Code *Settings*, search for `nautilus.cliPath`, and
  paste the path.
- Or close VS Code and start it from a terminal with `code .` in the
  project folder.

### "address already in use" when running

Something else is using port 8080. Stop the other program, or run on
another port. In `nautilus.yaml`, change `addr: "localhost:8080"` to
`addr: "localhost:8090"`, and use that port in every address in this
tutorial.

### The test says "no tag MotorRun in this project"

The tag is not declared in `nautilus.yaml`, or the name differs in spelling
or capital letters. Names are case sensitive.

### The program faults with "undefined tag"

A program read a tag before anything wrote it. Give every tag you read an
`init:` value in `nautilus.yaml`, as this tutorial does.

### The screen shows "no connection"

The controller is not running, or the screen is talking to a different
port. Start `naut run` and reload.

