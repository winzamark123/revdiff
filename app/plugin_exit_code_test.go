package main

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cmdReq struct {
	dir   string
	name  string
	stdin string
	args  []string
	env   map[string]string
}

type cmdResult struct {
	stdout string
	stderr string
	code   int
}

type launcherBackend struct {
	name    string
	command string
	env     map[string]string
}

type launcherRun struct {
	backend launcherBackend
	code    int
	output  string
}

func TestShellLaunchersPreserveAnnotationExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell launchers are not used on windows")
	}

	root := testRepoRoot(t)
	planFile := filepath.Join(t.TempDir(), "plan.md")
	writeTestFile(t, planFile, "# Plan\n")

	launchers := []struct {
		name string
		path string
		args []string
	}{
		{name: "claude", path: ".claude-plugin/skills/revdiff/scripts/launch-revdiff.sh"},
		{name: "codex", path: "plugins/codex/skills/revdiff/scripts/launch-revdiff.sh"},
		{name: "plan review", path: "plugins/revdiff-planning/scripts/launch-plan-review.sh", args: []string{planFile}},
	}
	cases := []struct {
		name   string
		code   int
		output string
	}{
		{name: "clean", code: 0},
		{name: "annotations", code: exitCodeAnnotations, output: "## file.go:1 (+)\ncomment\n"},
		{name: "failure", code: 1, output: "partial output\n"},
	}

	for _, launcher := range launchers {
		for _, backend := range launcherBackends() {
			t.Run(launcher.name+"/"+backend.name, func(t *testing.T) {
				for _, tc := range cases {
					t.Run(tc.name, func(t *testing.T) {
						script := filepath.Join(root, launcher.path)
						args := append([]string{script}, launcher.args...)
						res := runTestCmd(t, cmdReq{
							dir:  root,
							name: "bash",
							args: args,
							env: fakeLauncherEnv(t, launcherRun{
								backend: backend,
								code:    tc.code,
								output:  tc.output,
							}),
						})
						assert.Equal(t, tc.code, res.code)
						assert.Equal(t, tc.output, res.stdout)
					})
				}
			})
		}
	}
}

func TestPlanReviewHookAnnotationExitCodes(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found")
	}

	root := testRepoRoot(t)
	hook := filepath.Join(root, "plugins", "revdiff-planning", "scripts", "plan-review-hook.py")
	cases := []struct {
		name          string
		code          int
		output        string
		wantExit      int
		wantStdout    string
		wantStderr    string
		wantSnapshots int
	}{
		{name: "clean", code: 0, wantStdout: "plan reviewed, no annotations", wantSnapshots: 0},
		{
			name:          "annotations",
			code:          exitCodeAnnotations,
			output:        "## plan.md:2 (+)\nrevise this\n",
			wantExit:      2,
			wantStderr:    "revise this",
			wantSnapshots: 1,
		},
		{name: "failure", code: 1, wantStdout: "launcher exited 1", wantSnapshots: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			pluginRoot := filepath.Join(tmp, "plugin")
			launcher := filepath.Join(tmp, "launch-plan-review.sh")
			binDir := filepath.Join(tmp, "bin")
			writeExecutable(t, launcher, testFixtureScript(t, "fake-stdout-launcher.sh"))
			writeExecutable(t, filepath.Join(binDir, "revdiff"), "#!/bin/sh\nexit 0\n")
			writeExecutable(t, filepath.Join(pluginRoot, "scripts", "resolve-launcher.sh"), resolverScript(launcher))

			res := runTestCmd(t, cmdReq{
				dir:   root,
				name:  python,
				args:  []string{hook},
				stdin: `{"tool_input":{"plan":"# Plan\n- item"}}`,
				env: map[string]string{
					"CLAUDE_PLUGIN_ROOT": pluginRoot,
					"CLAUDE_PROJECT_DIR": root,
					"FAKE_OUTPUT":        tc.output,
					"FAKE_RC":            strconv.Itoa(tc.code),
					"PATH":               binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
					"TMPDIR":             tmp,
				},
			})
			assert.Equal(t, tc.wantExit, res.code)
			if tc.wantStdout == "" {
				assert.Empty(t, res.stdout)
			} else {
				assert.Contains(t, res.stdout, tc.wantStdout)
			}
			if tc.wantStderr == "" {
				assert.Empty(t, res.stderr)
			} else {
				assert.Contains(t, res.stderr, tc.wantStderr)
			}
			assert.Len(t, planSnapshots(t, tmp), tc.wantSnapshots)
		})
	}
}

func TestOpenCodeCallersPreserveAnnotationExitCode(t *testing.T) {
	root := testRepoRoot(t)
	tool := readRepoFile(t, root, "plugins", "opencode", "tools", "revdiff.ts")
	assert.Contains(t, tool, "const EXIT_CODE_ANNOTATIONS = 10;")
	assert.Contains(t, tool, ".quiet()")
	assert.Contains(t, tool, ".nothrow()")
	assert.Contains(t, tool, "if (!isRevdiffSuccess(result.exitCode))")
	assert.Contains(t, tool, "return stdout || \"(no annotations)\";")
	assert.Contains(t, tool, "return exitCode === 0 || exitCode === EXIT_CODE_ANNOTATIONS;")

	plugin := readRepoFile(t, root, "plugins", "opencode", "plugins", "revdiff-plan-review.ts")
	assert.Contains(t, plugin, "const EXIT_CODE_ANNOTATIONS = 10;")
	assert.Contains(t, plugin, ".quiet().nothrow()")
	assert.Contains(t, plugin, "if (isRevdiffSuccess(result.exitCode))")
	assert.Contains(t, plugin, "return stdout;")
	assert.Contains(t, plugin, "return exitCode === 0 || exitCode === EXIT_CODE_ANNOTATIONS;")
	assert.Contains(t, plugin, "if (!annotations) return;")
}

func TestPiCommandIsUserOnly(t *testing.T) {
	root := testRepoRoot(t)
	src := readRepoFile(t, root, "plugins", "pi", "extensions", "revdiff.ts")
	assert.Contains(t, src, `const EXIT_CODE_ON_ANNOTATIONS_ENV = "REVDIFF_EXIT_CODE_ON_ANNOTATIONS";`)
	assert.Contains(t, src, `const LINE_NUMBERS_ENV = "REVDIFF_LINE_NUMBERS";`)
	assert.Contains(t, src, "const commandArgs = [...launch.args, `--output=${outputFile}`];")
	assert.Contains(t, src, `[LINE_NUMBERS_ENV]: env[LINE_NUMBERS_ENV] ?? "true"`)
	assert.Contains(t, src, `pi.registerCommand("diff", {`)
	assert.Contains(t, src, `"split-window"`)
	assert.Contains(t, src, `"-h"`)
	assert.Contains(t, src, `"50%"`)
	assert.Contains(t, src, `pi.on("session_shutdown"`)
	assert.Contains(t, src, `{ deliverAs: "followUp" }`)
	assert.Contains(t, src, "focusTmuxPane(activeReview)")
	assert.Contains(t, src, "return exitCode === 0 || exitCode === EXIT_CODE_ANNOTATIONS;")
	assert.NotContains(t, src, "ctx.ui.custom")
	assert.NotContains(t, src, "registerTool")
	assert.NotContains(t, src, "revdiff_review")

	manifest := readRepoFile(t, root, "package.json")
	assert.NotContains(t, manifest, `"skills"`)
	assert.NoFileExists(t, filepath.Join(root, "plugins", "pi", "skills", "revdiff", "SKILL.md"))
}

func TestPiExecutableRegressionHasCIBunSetup(t *testing.T) {
	root := testRepoRoot(t)
	ci := readRepoFile(t, root, ".github", "workflows", "ci.yml")
	assert.Contains(t, ci, "oven-sh/setup-bun")
	assert.Contains(t, ci, "go test -race")
}

func TestPiExtensionExecutableBehavior(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed locally; CI installs Bun via oven-sh/setup-bun and runs this regression")
	}

	root := testRepoRoot(t)
	tmp := t.TempDir()
	testPath := filepath.Join(tmp, "plugins", "pi", "extensions", "revdiff-test.ts")
	writeTestFile(t, testPath, readRepoFile(t, root, "plugins", "pi", "extensions", "revdiff.ts")+piExtensionHarness())

	res := runTestCmd(t, cmdReq{
		dir:  tmp,
		name: bun,
		args: []string{"run", testPath},
		env:  map[string]string{"PATH": os.Getenv("PATH")},
	})
	require.Equal(t, 0, res.code, "stdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
}

func piExtensionHarness() string {
	return `
import { chmodSync as testChmodSync, mkdirSync as testMkdirSync, realpathSync as testRealpathSync, writeFileSync as testWriteFileSync } from "node:fs";

function testAssert(condition: unknown, message: string): asserts condition {
	if (!condition) {
		throw new Error(message);
	}
}

function assertArray(actual: string[], expected: string[], message: string): void {
	const actualText = JSON.stringify(actual);
	const expectedText = JSON.stringify(expected);
	testAssert(actualText === expectedText, message + ": got " + actualText + ", want " + expectedText);
}

function fakeCtx(choice?: "uncommitted" | "branch", cwd = process.cwd(), idle = true) {
	return {
		hasUI: true,
		mode: "tui",
		cwd,
		isIdle: () => idle,
		ui: {
			notifications: [] as string[],
			notify(message: string) {
				this.notifications.push(message);
			},
			select(_title: string, choices: string[]) {
				return choice === "branch" ? choices[1] : choices[0];
			},
		},
	} as any;
}

function fakePi() {
	const commands = new Map<string, any>();
	const handlers = new Map<string, Array<() => void>>();
	const sentMessages: Array<{ message: string; options?: { deliverAs: string } }> = [];
	return {
		commands,
		handlers,
		sentMessages,
		on(name: string, handler: () => void) {
			handlers.set(name, [...(handlers.get(name) ?? []), handler]);
		},
		registerCommand(name: string, command: any) {
			commands.set(name, command);
		},
		sendUserMessage(message: string, options?: { deliverAs: string }) {
			sentMessages.push({ message, options });
		},
	} as any;
}

function writeExecutable(pathname: string, content: string): void {
	testMkdirSync(path.dirname(pathname), { recursive: true });
	testWriteFileSync(pathname, content);
	testChmodSync(pathname, 0o700);
}

function fakeTmuxScript(): string {
	return [
		"#!/bin/sh",
		"command=$1",
		"shift",
		"case \"$command\" in",
		"  split-window)",
		"    printf '%s\\n' \"$@\" > \"$FAKE_TMUX_ARGS_FILE\"",
		"    launch=",
		"    for arg in \"$@\"; do launch=$arg; done",
		"    : > \"$FAKE_TMUX_PANE_FILE\"",
		"    (sh -c \"$launch\"; rm -f \"$FAKE_TMUX_PANE_FILE\") &",
		"    echo '%99'",
		"    ;;",
		"  display-message)",
		"    test -f \"$FAKE_TMUX_PANE_FILE\"",
		"    ;;",
		"  select-pane)",
		"    test -f \"$FAKE_TMUX_PANE_FILE\" || exit 1",
		"    : > \"$FAKE_TMUX_FOCUS_FILE\"",
		"    ;;",
		"  kill-pane)",
		"    rm -f \"$FAKE_TMUX_PANE_FILE\"",
		"    ;;",
		"  *) exit 2;;",
		"esac",
		"",
	].join("\n");
}

function fakeRevdiffScript(): string {
	return [
		"#!/bin/sh",
		"test \"$REVDIFF_EXIT_CODE_ON_ANNOTATIONS\" = \"true\" || exit 21",
		"test \"$REVDIFF_LINE_NUMBERS\" = \"true\" || exit 23",
		"out=",
		"for arg in \"$@\"; do",
		"  case \"$arg\" in --output=*) out=${arg#--output=};; esac",
		"done",
		"test -n \"$out\" || exit 22",
		"printf '%s\\n' \"$@\" > \"$FAKE_ARG_FILE\"",
		"pwd > \"$FAKE_CWD_FILE\"",
		"cp \"$FAKE_INITIAL_ANNOTATIONS_FILE\" \"$out.tmp\" && mv -f \"$out.tmp\" \"$out\"",
		"sleep 0.1",
		"cp \"$FAKE_INITIAL_ANNOTATIONS_FILE\" \"$out.tmp\" && mv -f \"$out.tmp\" \"$out\"",
		"while [ ! -f \"$FAKE_EXIT_SIGNAL\" ]; do sleep 0.02; done",
		"cp \"$FAKE_FINAL_ANNOTATIONS_FILE\" \"$out.tmp\" && mv -f \"$out.tmp\" \"$out\"",
		"exit 10",
		"",
	].join("\n");
}

async function waitFor(condition: () => boolean, message: string): Promise<void> {
	const deadline = Date.now() + 3000;
	while (!condition()) {
		if (Date.now() >= deadline) {
			throw new Error(message);
		}
		await Bun.sleep(20);
	}
}

function testPiReviewEnv(): void {
	const defaults = withPiReviewEnv({});
	testAssert(defaults.REVDIFF_EXIT_CODE_ON_ANNOTATIONS === "true", "pi should request annotation exit codes");
	testAssert(defaults.REVDIFF_LINE_NUMBERS === "true", "pi should enable line numbers by default");

	const overridden = withPiReviewEnv({ REVDIFF_LINE_NUMBERS: "false" });
	testAssert(overridden.REVDIFF_LINE_NUMBERS === "false", "pi should preserve an explicit line-number preference");
}

async function testSidePaneFlushFocusAndExitDelivery(): Promise<void> {
	const tempDir = mkdtempSync(path.join(tmpdir(), "pi-revdiff-side-pane-"));
	const binDir = path.join(tempDir, "bin");
	const targetRepo = path.join(tempDir, "repo with spaces");
	const argFile = path.join(tempDir, "args.txt");
	const cwdFile = path.join(tempDir, "cwd.txt");
	const paneFile = path.join(tempDir, "pane");
	const focusFile = path.join(tempDir, "focus");
	const tmuxArgsFile = path.join(tempDir, "tmux-args.txt");
	const exitSignal = path.join(tempDir, "exit");
	const initialAnnotations = path.join(tempDir, "initial.md");
	const finalAnnotations = path.join(tempDir, "final.md");
	writeExecutable(path.join(binDir, "tmux"), fakeTmuxScript());
	writeExecutable(path.join(binDir, "revdiff"), fakeRevdiffScript());
	testMkdirSync(targetRepo, { recursive: true });
	testWriteFileSync(path.join(targetRepo, "README.md"), "hello\n");
	testWriteFileSync(initialAnnotations, "## src/app.go:12-14 (+)\nfix it\n");
	testWriteFileSync(finalAnnotations, "## src/app.go:12-14 (+)\nfix it\n\n## src/store.go:7 (+)\nadd a guard\n");

	const envKeys = [
		"PATH",
		"REVDIFF_BIN",
		"TMUX",
		"TMUX_PANE",
		"FAKE_ARG_FILE",
		"FAKE_CWD_FILE",
		"FAKE_TMUX_ARGS_FILE",
		"FAKE_TMUX_PANE_FILE",
		"FAKE_TMUX_FOCUS_FILE",
		"FAKE_EXIT_SIGNAL",
		"FAKE_INITIAL_ANNOTATIONS_FILE",
		"FAKE_FINAL_ANNOTATIONS_FILE",
	] as const;
	const oldEnv = new Map(envKeys.map((key) => [key, process.env[key]]));
	Object.assign(process.env, {
		PATH: binDir + path.delimiter + process.env.PATH,
		REVDIFF_BIN: path.join(binDir, "revdiff"),
		TMUX: "test",
		TMUX_PANE: "%1",
		FAKE_ARG_FILE: argFile,
		FAKE_CWD_FILE: cwdFile,
		FAKE_TMUX_ARGS_FILE: tmuxArgsFile,
		FAKE_TMUX_PANE_FILE: paneFile,
		FAKE_TMUX_FOCUS_FILE: focusFile,
		FAKE_EXIT_SIGNAL: exitSignal,
		FAKE_INITIAL_ANNOTATIONS_FILE: initialAnnotations,
		FAKE_FINAL_ANNOTATIONS_FILE: finalAnnotations,
	});

	try {
		const pi = fakePi();
		const ctx = fakeCtx(undefined, targetRepo, false);
		revdiffExtension(pi);
		testAssert(pi.commands.has("diff"), "expected /diff to be registered");
		testAssert(!pi.commands.has("revdiff"), "expected /revdiff to be removed");

		await pi.commands.get("diff").handler("README.md", ctx);
		await waitFor(() => pi.sentMessages.length === 1, "O flush should send annotations while revdiff remains open");
		testAssert(pi.sentMessages[0].message.includes("Address the 1 revdiff annotation for README.md."), "flush should summarize the review");
		testAssert(pi.sentMessages[0].message.includes("## src/app.go:12-14 (+)\nfix it"), "flush should include annotations");
		testAssert(pi.sentMessages[0].options?.deliverAs === "followUp", "busy pi sessions should receive annotations as a follow-up");

		await Bun.sleep(180);
		testAssert(pi.sentMessages.length === 1, "an unchanged O snapshot should not be sent twice");
		await pi.commands.get("diff").handler("HEAD~1", ctx);
		await waitFor(() => existsSync(focusFile), "a second /diff should focus the existing revdiff pane");
		testAssert(pi.sentMessages.length === 1, "focusing the existing pane should not start another review");

		testWriteFileSync(exitSignal, "done\n");
		await waitFor(() => pi.sentMessages.length === 2, "q should send a final annotation snapshot that was not flushed");
		await waitFor(
			() => ctx.ui.notifications.some((message: string) => message.includes("Review pane closed")),
			"closing revdiff should finish the review session",
		);
		testAssert(pi.sentMessages[1].message.includes("Address the 2 revdiff annotations for README.md."), "final snapshot should include both annotations");
		testAssert(
			testRealpathSync(readFileSync(cwdFile, "utf8").trim()) === testRealpathSync(targetRepo),
			"revdiff should launch in the command working directory",
		);
		const argText = readFileSync(argFile, "utf8");
		testAssert(argText.includes("--only\nREADME.md\n"), "file target should resolve to --only README.md");
		const tmuxArgs = readFileSync(tmuxArgsFile, "utf8");
		testAssert(tmuxArgs.includes("-h\n-l\n50%\n"), "revdiff should open in a 50 percent right-side pane");
	} finally {
		for (const key of envKeys) {
			const value = oldEnv.get(key);
			if (value === undefined) {
				delete process.env[key];
			} else {
				process.env[key] = value;
			}
		}
		rmSync(tempDir, { recursive: true, force: true });
	}
}

async function testArgumentResolution(): Promise<void> {
	const cwd = process.cwd();
	let launch = await resolveLaunchSpec("--output ignored --only 'docs/my plan.md'", fakeCtx(), cwd);
	testAssert(Boolean(launch), "expected launch after stripping --output");
	assertArray(launch!.args, ["--only", "docs/my plan.md"], "--output stripping should preserve remaining args");
	testAssert(launch!.label === "docs/my plan.md", "--only label should use target path");

	launch = await resolveLaunchSpec("all-files exclude vendor and dist", fakeCtx(), cwd);
	testAssert(Boolean(launch), "expected all-files shortcut launch");
	assertArray(launch!.args, ["--all-files", "--exclude=vendor", "--exclude=dist"], "all-files shortcut should expand excludes");

	launch = await resolveLaunchSpec("./docs/new-file.md", fakeCtx(), cwd);
	testAssert(Boolean(launch), "expected explicit path file launch");
	assertArray(launch!.args, ["--only", "./docs/new-file.md"], "explicit path arg should map to --only");

	launch = await resolveLaunchSpec("release/v1.2.3", fakeCtx(), cwd);
	testAssert(Boolean(launch), "expected slash-dot token launch");
	assertArray(launch!.args, ["release/v1.2.3"], "slash-dot token should stay a ref-like arg when path does not exist");

	const roundTrip = ["--description=why this matters", "--only", "docs/it's mine.md"];
	assertArray(shellSplit(shellJoin(roundTrip)), roundTrip, "shellJoin output should shellSplit back to original args");
}

async function testRefLikePathArgKeepsRef(): Promise<void> {
	const oldCwd = process.cwd();
	const repo = initGitRepo();
	try {
		runGit(repo, ["checkout", "-b", "release/v1.2.3"]);
		process.chdir(repo);
		const launch = await resolveLaunchSpec("release/v1.2.3", fakeCtx(), repo);
		testAssert(Boolean(launch), "expected ref-like launch");
		assertArray(launch!.args, ["release/v1.2.3"], "ref-like path arg should stay a ref when the git ref exists");
		testAssert(launch!.label === "release/v1.2.3", "ref-like path label should stay the ref");
	} finally {
		process.chdir(oldCwd);
		rmSync(repo, { recursive: true, force: true });
	}
}

function runGit(repo: string, args: string[]): void {
	const result = spawnSync("git", args, { cwd: repo, encoding: "utf8" });
	testAssert(result.status === 0, "git " + args.join(" ") + " failed: " + (result.stderr || result.stdout));
}

function initGitRepo(): string {
	const repo = mkdtempSync(path.join(tmpdir(), "pi-revdiff-git-"));
	runGit(repo, ["init"]);
	runGit(repo, ["checkout", "-b", "main"]);
	runGit(repo, ["config", "user.email", "test@example.com"]);
	runGit(repo, ["config", "user.name", "Test User"]);
	testWriteFileSync(path.join(repo, "file.txt"), "base\n");
	runGit(repo, ["add", "file.txt"]);
	runGit(repo, ["commit", "-m", "initial"]);
	return repo;
}

async function testNeedsAskWithoutMainStops(): Promise<void> {
	const detectScript = path.resolve("plugins", "pi", "scripts", "detect-ref.sh");
	writeExecutable(
		detectScript,
		[
			"#!/bin/sh",
			"echo 'branch: @'",
			"echo 'main_branch: '",
			"echo 'is_main: false'",
			"echo 'has_uncommitted: false'",
			"echo 'has_staged_only: false'",
			"echo 'suggested_ref: '",
			"echo 'use_staged: false'",
			"echo 'needs_ask: true'",
			"",
		].join("\n"),
	);

	try {
		const ctx = fakeCtx();
		const launch = await detectSmartLaunch(ctx, process.cwd());
		testAssert(launch === undefined, "needsAsk without a main branch should not launch uncommitted review");
		testAssert(
			ctx.ui.notifications.some((message: string) => message.includes("Could not determine a revdiff target")),
			"needsAsk without a main branch should notify a clear target error",
		);
	} finally {
		rmSync(path.resolve("plugins", "pi", "scripts"), { recursive: true, force: true });
	}
}

async function testStagedSmartDetection(): Promise<void> {
	const oldCwd = process.cwd();
	const mainRepo = initGitRepo();
	const featureRepo = initGitRepo();
	try {
		testWriteFileSync(path.join(mainRepo, "file.txt"), "main staged\n");
		runGit(mainRepo, ["add", "file.txt"]);
		let launch = await detectSmartLaunch(fakeCtx(), mainRepo);
		testAssert(Boolean(launch), "expected staged launch on main");
		assertArray(launch!.args, ["--staged"], "main staged-only should launch --staged");
		testAssert(launch!.label === "staged changes", "main staged-only label should be staged changes");

		runGit(featureRepo, ["checkout", "-b", "feature"]);
		testWriteFileSync(path.join(featureRepo, "file.txt"), "feature staged\n");
		runGit(featureRepo, ["add", "file.txt"]);
		launch = await detectSmartLaunch(fakeCtx("uncommitted"), featureRepo);
		testAssert(Boolean(launch), "expected dirty feature uncommitted launch");
		assertArray(launch!.args, ["--staged"], "dirty feature uncommitted choice should launch --staged");

		launch = await detectSmartLaunch(fakeCtx("branch"), featureRepo);
		testAssert(Boolean(launch), "expected dirty feature branch launch");
		assertArray(launch!.args, ["main"], "dirty feature branch choice should preserve branch diff");
		testAssert(launch!.label === "feature vs main", "dirty feature branch label should identify main branch");
	} finally {
		process.chdir(oldCwd);
		rmSync(mainRepo, { recursive: true, force: true });
		rmSync(featureRepo, { recursive: true, force: true });
	}
}

testPiReviewEnv();
await testSidePaneFlushFocusAndExitDelivery();
await testArgumentResolution();
await testRefLikePathArgKeepsRef();
await testNeedsAskWithoutMainStops();
await testStagedSmartDetection();
console.log("pi extension executable behavior ok");
`
}

func testRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Dir(wd)
}

func runTestCmd(t *testing.T, r cmdReq) cmdResult {
	t.Helper()
	cmd := exec.Command(r.name, r.args...) //nolint:gosec // tests execute fixed repo scripts and temp fixtures
	cmd.Dir = r.dir
	cmd.Env = mergeEnv(r.env)
	if r.stdin != "" {
		cmd.Stdin = strings.NewReader(r.stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return cmdResult{stdout: stdout.String(), stderr: stderr.String(), code: commandExitCode(err)}
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func mergeEnv(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return os.Environ()
	}
	keys := make(map[string]struct{}, len(overrides))
	for k := range overrides {
		keys[k] = struct{}{}
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if _, found := keys[key]; ok && found {
			continue
		}
		env = append(env, item)
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	writeTestFile(t, path, content)
	require.NoError(t, os.Chmod(path, 0o700)) //nolint:gosec // test fixtures must be executable
}

func readRepoFile(t *testing.T, root string, elems ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{root}, elems...)...))
	require.NoError(t, err)
	return string(b)
}

func testFixtureScript(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "plugin-exit-code", name)) //nolint:gosec // tests read fixed fixture files
	require.NoError(t, err)
	return string(b)
}

func launcherBackends() []launcherBackend {
	return []launcherBackend{
		{name: "tmux", command: "tmux", env: map[string]string{"TMUX": "1"}},
		{name: "zellij", command: "zellij", env: map[string]string{"ZELLIJ": "1"}},
		{name: "herdr", command: "herdr", env: map[string]string{"HERDR_ENV": "1"}},
		{name: "kitty", command: "kitty", env: map[string]string{"KITTY_LISTEN_ON": "unix:/tmp/kitty"}},
		{name: "wezterm", command: "wezterm", env: map[string]string{"WEZTERM_PANE": "1"}},
		{name: "cmux", command: "cmux", env: map[string]string{"CMUX_SURFACE_ID": "1"}},
		{name: "cmux ghostty env", command: "cmux", env: map[string]string{
			"TERM_PROGRAM":          "ghostty",
			"__CFBundleIdentifier":  "com.cmuxterm.app",
			"GHOSTTY_RESOURCES_DIR": "/Applications/cmux.app/Contents/Resources/ghostty",
			"GHOSTTY_BIN_DIR":       "/Applications/cmux.app/Contents/MacOS",
		}},
		{name: "ghostty", command: "osascript", env: map[string]string{"TERM_PROGRAM": "ghostty"}},
		{name: "iterm2", command: "osascript", env: map[string]string{"ITERM_SESSION_ID": "w0t0p0:ABC"}},
		{name: "emacs", command: "emacsclient", env: map[string]string{"INSIDE_EMACS": "vterm"}},
	}
}

func fakeLauncherEnv(t *testing.T, r launcherRun) map[string]string {
	t.Helper()
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	writeExecutable(t, filepath.Join(binDir, "revdiff"), testFixtureScript(t, "fake-revdiff-output.sh"))
	writeExecutable(t, filepath.Join(binDir, r.backend.command), testFixtureScript(t, "fake-overlay-backend.sh"))

	env := cleanOverlayEnv()
	maps.Copy(env, r.backend.env)
	env["FAKE_OUTPUT"] = r.output
	env["FAKE_RC"] = strconv.Itoa(r.code)
	env["PATH"] = binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	env["TMPDIR"] = tmp
	return env
}

func cleanOverlayEnv() map[string]string {
	return map[string]string{
		"TMUX":                  "",
		"ZELLIJ":                "",
		"HERDR_ENV":             "",
		"HERDR_SOCKET_PATH":     "",
		"HERDR_PANE_ID":         "",
		"KITTY_LISTEN_ON":       "",
		"KITTY_WINDOW_ID":       "",
		"WEZTERM_PANE":          "",
		"CMUX_SURFACE_ID":       "",
		"TERM_PROGRAM":          "",
		"GHOSTTY_SURFACE_ID":    "",
		"GHOSTTY_RESOURCES_DIR": "",
		"GHOSTTY_BIN_DIR":       "",
		"__CFBundleIdentifier":  "",
		"ITERM_SESSION_ID":      "",
		"INSIDE_EMACS":          "",
		"AGTERM_SESSION_ID":     "",
		"AGTERM_SOCKET":         "",
		"REVDIFF_CONFIG":        "",
	}
}

func resolverScript(launcher string) string {
	return "#!/bin/sh\nprintf '%s\\n' " + shQuote(launcher) + "\n"
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func planSnapshots(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "plan-rev-*.md"))
	require.NoError(t, err)
	return matches
}
