import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, watch, writeFileSync, type FSWatcher } from "node:fs";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";

import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

const EXIT_CODE_ANNOTATIONS = 10;
const EXIT_CODE_ON_ANNOTATIONS_ENV = "REVDIFF_EXIT_CODE_ON_ANNOTATIONS";
const LINE_NUMBERS_ENV = "REVDIFF_LINE_NUMBERS";
const ANNOTATION_HEADER_RE = /^## (.+?)(?::(\d+)(?:-\d+)?)? \(([^)]+)\)$/;
const EXT_DIR = path.dirname(fileURLToPath(import.meta.url));
const PI_PLUGIN_ROOT = path.resolve(EXT_DIR, "..");
const REPO_ROOT = path.resolve(PI_PLUGIN_ROOT, "..", "..");
const DETECT_REF_SCRIPT = path.join(PI_PLUGIN_ROOT, "scripts", "detect-ref.sh");

interface AnnotationItem {
	file: string;
	line?: number;
	kind: string;
	text: string;
}

interface ReviewResult {
	label: string;
	rawOutput: string;
	annotations: AnnotationItem[];
}

interface LaunchSpec {
	args: string[];
	label: string;
}

interface SmartDetectResult {
	branch: string;
	mainBranch: string;
	isMain: boolean;
	hasUncommitted: boolean;
	useStaged: boolean;
	suggestedRef: string;
	needsAsk: boolean;
}

interface ActiveReview {
	label: string;
	lastOutput: string;
	outputFile: string;
	paneId: string;
	pollTimer?: ReturnType<typeof setInterval>;
	sentinelFile: string;
	tempDir: string;
	tmuxBin: string;
	watcher: FSWatcher;
}

export default function revdiffExtension(pi: ExtensionAPI): void {
	let activeReview: ActiveReview | undefined;

	function stopReview(review: ActiveReview, killPane: boolean): void {
		review.watcher.close();
		if (review.pollTimer) {
			clearInterval(review.pollTimer);
		}
		if (killPane && tmuxPaneExists(review)) {
			spawnSync(review.tmuxBin, ["kill-pane", "-t", review.paneId], { env: process.env, stdio: "ignore" });
		}
		rmSync(review.tempDir, { recursive: true, force: true });
		if (activeReview === review) {
			activeReview = undefined;
		}
	}

	function deliverOutput(review: ActiveReview, ctx: ExtensionContext): void {
		if (!existsSync(review.outputFile)) {
			return;
		}
		const rawOutput = readFileSync(review.outputFile, "utf8").trim();
		if (!rawOutput || rawOutput === review.lastOutput) {
			return;
		}

		const result = {
			label: review.label,
			rawOutput,
			annotations: parseAnnotations(rawOutput),
		};
		if (result.annotations.length === 0) {
			return;
		}

		try {
			pi.sendUserMessage(reviewMessage(result), ctx.isIdle() ? undefined : { deliverAs: "followUp" });
			review.lastOutput = rawOutput;
		} catch (error) {
			ctx.ui.notify(`Failed to send revdiff annotations: ${error instanceof Error ? error.message : String(error)}`, "error");
		}
	}

	function finishReview(review: ActiveReview, ctx: ExtensionContext): void {
		const outputExists = existsSync(review.outputFile) && Boolean(readFileSync(review.outputFile, "utf8").trim());
		deliverOutput(review, ctx);
		const exitCode = Number.parseInt(readFileSync(review.sentinelFile, "utf8").trim(), 10);
		stopReview(review, false);

		if (!isRevdiffSuccess(exitCode)) {
			ctx.ui.notify(`revdiff exited with code ${Number.isNaN(exitCode) ? "unknown" : exitCode}`, "warning");
			return;
		}
		if (exitCode === EXIT_CODE_ANNOTATIONS && !outputExists) {
			ctx.ui.notify("revdiff reported annotations without writing output", "warning");
			return;
		}
		ctx.ui.notify(
			review.lastOutput ? `Review pane closed for ${review.label}.` : `Review complete — no annotations for ${review.label}.`,
			"info",
		);
	}

	pi.on("session_shutdown", () => {
		if (activeReview) {
			stopReview(activeReview, true);
		}
	});

	pi.registerCommand("diff", {
		description: "Open revdiff in a right-side pane for an explicit interactive review",
		handler: async (args, ctx) => {
			if (ctx.mode !== "tui") {
				return;
			}

			if (activeReview) {
				if (focusTmuxPane(activeReview)) {
					return;
				}
				deliverOutput(activeReview, ctx);
				stopReview(activeReview, false);
			}

			const launch = await resolveLaunchSpec(args.trim(), ctx, ctx.cwd);
			if (!launch) {
				return;
			}

			const review = startSideReview({ launch, cwd: ctx.cwd, ctx });
			if (!review) {
				return;
			}
			activeReview = review;

			const processFiles = () => {
				if (activeReview !== review) {
					return;
				}
				deliverOutput(review, ctx);
				if (existsSync(review.sentinelFile)) {
					finishReview(review, ctx);
				}
			};
			review.watcher.on("change", processFiles);
			review.watcher.on("error", (error) => {
				if (activeReview !== review) {
					return;
				}
				stopReview(review, true);
				ctx.ui.notify(`Failed to watch revdiff annotations: ${error.message}`, "error");
			});
			review.pollTimer = setInterval(() => {
				processFiles();
				if (activeReview === review && !tmuxPaneExists(review) && !existsSync(review.sentinelFile)) {
					stopReview(review, false);
					ctx.ui.notify(`Review pane closed unexpectedly for ${review.label}.`, "warning");
				}
			}, 300);
			processFiles();
		},
	});
}

function reviewMessage(result: ReviewResult): string {
	const noun = result.annotations.length === 1 ? "annotation" : "annotations";
	return [
		`Address the ${result.annotations.length} revdiff ${noun} for ${result.label}.`,
		"Do not launch or reopen revdiff; only the user may start it with /diff.",
		"",
		result.rawOutput.trim(),
	].join("\n");
}

async function resolveLaunchSpec(rawArgs: string, ctx: ExtensionContext, cwd: string): Promise<LaunchSpec | undefined> {
	const trimmed = rawArgs.trim();
	if (!trimmed) {
		return detectSmartLaunch(ctx, cwd);
	}

	const split = shellSplit(trimmed);
	if (split.length === 0) {
		return detectSmartLaunch(ctx, cwd);
	}

	const allFiles = parseAllFilesShortcut(split.join(" "));
	if (allFiles) {
		return allFiles;
	}

	const tokens = sanitizeArgs(split);
	if (tokens.length === 0) {
		ctx.ui.notify("No revdiff arguments left after stripping --output", "warning");
		return undefined;
	}

	if (tokens.length === 1 && isFileReviewArg(tokens[0]!, cwd)) {
		const target = tokens[0]!;
		return { args: ["--only", target], label: target };
	}

	return { args: tokens, label: describeArgs(tokens) };
}

async function detectSmartLaunch(ctx: ExtensionContext, cwd: string): Promise<LaunchSpec | undefined> {
	const detected = detectSmartRef(cwd);
	if (!detected) {
		ctx.ui.notify("Not inside a supported VCS repo or smart detection is unavailable. Use /diff --only <file> to review a standalone file.", "warning");
		return undefined;
	}

	if (detected.needsAsk) {
		if (!detected.mainBranch) {
			ctx.ui.notify("Could not determine a revdiff target. Pass a ref or use /diff --only <file>.", "warning");
			return undefined;
		}
		const choice = await ctx.ui.select("revdiff", [
			"Review uncommitted changes only",
			`Review current branch against ${detected.mainBranch}`,
		]);
		if (!choice) {
			return undefined;
		}
		if (choice.startsWith("Review uncommitted")) {
			return uncommittedLaunchSpec(detected);
		}
		return { args: [detected.mainBranch], label: `${detected.branch} vs ${detected.mainBranch}` };
	}

	if (!detected.suggestedRef) {
		return uncommittedLaunchSpec(detected);
	}

	if (detected.mainBranch && detected.suggestedRef === detected.mainBranch && !detected.isMain) {
		return { args: [detected.mainBranch], label: `${detected.branch} vs ${detected.mainBranch}` };
	}

	return { args: [detected.suggestedRef], label: detected.suggestedRef };
}

function uncommittedLaunchSpec(detected: SmartDetectResult): LaunchSpec {
	if (detected.useStaged) {
		return { args: ["--staged"], label: "staged changes" };
	}
	return { args: [], label: "uncommitted changes" };
}

interface StartSideReviewParams {
	launch: LaunchSpec;
	cwd: string;
	ctx: ExtensionContext;
}

function startSideReview({ launch, cwd, ctx }: StartSideReviewParams): ActiveReview | undefined {
	const revdiffBin = resolveRevdiffBin();
	if (!revdiffBin) {
		ctx.ui.notify("revdiff binary not found. Install it or set REVDIFF_BIN.", "error");
		return undefined;
	}
	const tmuxBin = findInPath("tmux");
	if (!process.env.TMUX || !process.env.TMUX_PANE || !tmuxBin) {
		ctx.ui.notify("The side-by-side revdiff workflow requires pi to run inside tmux.", "error");
		return undefined;
	}

	const tempDir = mkdtempSync(path.join(tmpdir(), "revdiff-pi-"));
	const outputFile = path.join(tempDir, "annotations.txt");
	const sentinelFile = path.join(tempDir, "exit-code");
	const launcherFile = path.join(tempDir, "launch.sh");
	const commandArgs = [...launch.args, `--output=${outputFile}`];
	const reviewEnv = withPiReviewEnv(process.env);
	writeFileSync(
		launcherFile,
		[
			"#!/bin/sh",
			`cd ${shellQuote(cwd)} || exit 1`,
			"rc=0",
			`${EXIT_CODE_ON_ANNOTATIONS_ENV}=${shellQuote(reviewEnv[EXIT_CODE_ON_ANNOTATIONS_ENV] ?? "true")} ${LINE_NUMBERS_ENV}=${shellQuote(reviewEnv[LINE_NUMBERS_ENV] ?? "true")} ${shellJoin([revdiffBin, ...commandArgs])} || rc=$?`,
			`printf '%s' "$rc" > ${shellQuote(`${sentinelFile}.tmp`)} && mv -f ${shellQuote(`${sentinelFile}.tmp`)} ${shellQuote(sentinelFile)}`,
			'exit "$rc"',
			"",
		].join("\n"),
		{ mode: 0o700 },
	);

	let watcher: FSWatcher;
	try {
		watcher = watch(tempDir);
	} catch (error) {
		rmSync(tempDir, { recursive: true, force: true });
		ctx.ui.notify(`Failed to watch revdiff annotations: ${error instanceof Error ? error.message : String(error)}`, "error");
		return undefined;
	}

	const split = spawnSync(
		tmuxBin,
		[
			"split-window",
			"-h",
			"-l",
			"50%",
			"-P",
			"-F",
			"#{pane_id}",
			"-t",
			process.env.TMUX_PANE,
			"-c",
			cwd,
			shellJoin([launcherFile]),
		],
		{ encoding: "utf8", env: process.env },
	);
	const paneId = split.stdout.trim();
	if (split.error || split.status !== 0 || !paneId) {
		watcher.close();
		rmSync(tempDir, { recursive: true, force: true });
		ctx.ui.notify(`Failed to open revdiff pane: ${split.error?.message ?? (split.stderr.trim() || `tmux exited ${split.status}`)}`, "error");
		return undefined;
	}

	ctx.ui.notify(`Opened revdiff for ${launch.label}. Press O to send annotations to pi.`, "info");
	return {
		label: launch.label,
		lastOutput: "",
		outputFile,
		paneId,
		sentinelFile,
		tempDir,
		tmuxBin,
		watcher,
	};
}

function tmuxPaneExists(review: ActiveReview): boolean {
	return spawnSync(review.tmuxBin, ["display-message", "-p", "-t", review.paneId, "#{pane_id}"], {
		env: process.env,
		stdio: "ignore",
	}).status === 0;
}

function focusTmuxPane(review: ActiveReview): boolean {
	if (!tmuxPaneExists(review)) {
		return false;
	}
	return spawnSync(review.tmuxBin, ["select-pane", "-t", review.paneId], { env: process.env, stdio: "ignore" }).status === 0;
}

// use env instead of CLI flags so old revdiff binaries ignore unsupported options;
// preserve an explicit line-number preference inherited from pi
function withPiReviewEnv(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
	return {
		...env,
		[EXIT_CODE_ON_ANNOTATIONS_ENV]: "true",
		[LINE_NUMBERS_ENV]: env[LINE_NUMBERS_ENV] ?? "true",
	};
}

function isRevdiffSuccess(exitCode: number): boolean {
	return exitCode === 0 || exitCode === EXIT_CODE_ANNOTATIONS;
}

function parseAnnotations(output: string): AnnotationItem[] {
	if (!output.trim()) {
		return [];
	}

	const annotations: AnnotationItem[] = [];
	let current:
		| {
				file: string;
				line?: number;
				kind: string;
				bodyLines: string[];
		  }
		| undefined;

	for (const rawLine of output.split(/\r?\n/)) {
		const match = ANNOTATION_HEADER_RE.exec(rawLine.trimEnd());
		if (!match) {
			if (current) {
				current.bodyLines.push(rawLine);
			}
			continue;
		}

		if (current) {
			annotations.push({
				file: current.file,
				line: current.line,
				kind: current.kind,
				text: current.bodyLines.join("\n").trim(),
			});
		}

		current = {
			file: match[1]!,
			line: match[2] ? Number.parseInt(match[2], 10) : undefined,
			kind: match[3]!,
			bodyLines: [],
		};
	}

	if (current) {
		annotations.push({
			file: current.file,
			line: current.line,
			kind: current.kind,
			text: current.bodyLines.join("\n").trim(),
		});
	}

	return annotations;
}

function describeArgs(args: string[]): string {
	if (args.length === 0) {
		return "uncommitted changes";
	}
	if (args.includes("--staged")) {
		return "staged changes";
	}
	if (args.includes("--all-files") || args.includes("-A")) {
		const excludes = collectFlagValues(args, "--exclude", "-X");
		if (excludes.length > 0) {
			return `all files, excluding ${excludes.join(", ")}`;
		}
		return "all files";
	}
	const onlyFiles = collectFlagValues(args, "--only", "-F");
	if (onlyFiles.length > 0) {
		return onlyFiles.length === 1 ? onlyFiles[0]! : `files: ${onlyFiles.join(", ")}`;
	}
	return shellJoin(args);
}

function parseAllFilesShortcut(raw: string): LaunchSpec | undefined {
	const match = /^all(?:-| )files(?:\s+exclude\s+(.+))?$/i.exec(raw);
	if (!match) {
		return undefined;
	}
	const args = ["--all-files"];
	const excludePart = match[1]?.trim();
	if (excludePart) {
		for (const part of excludePart.split(/\s*(?:,|and)\s*/)) {
			if (part) {
				args.push(`--exclude=${part}`);
			}
		}
	}
	return { args, label: describeArgs(args) };
}

function collectFlagValues(args: string[], longFlag: string, shortFlag: string): string[] {
	const values: string[] = [];
	for (let i = 0; i < args.length; i++) {
		const arg = args[i]!;
		if (arg === longFlag || arg === shortFlag) {
			if (i + 1 < args.length) {
				values.push(args[i + 1]!);
				i++;
			}
			continue;
		}
		if (arg.startsWith(`${longFlag}=`)) {
			values.push(arg.slice(longFlag.length + 1));
		}
	}
	return values;
}

function sanitizeArgs(args: string[]): string[] {
	const sanitized: string[] = [];
	let skipNext = false;
	for (const arg of args) {
		if (skipNext) {
			skipNext = false;
			continue;
		}
		if (arg === "-o" || arg === "--output") {
			skipNext = true;
			continue;
		}
		if (arg.startsWith("--output=")) {
			continue;
		}
		sanitized.push(arg);
	}
	return sanitized;
}

function isFileReviewArg(arg: string, cwd: string): boolean {
	if (arg.startsWith("-")) {
		return false;
	}
	if (existsSync(path.resolve(cwd, arg))) {
		return true;
	}
	return arg.startsWith("/") || arg.startsWith("./") || arg.startsWith("../");
}

function shellSplit(input: string): string[] {
	const tokens: string[] = [];
	let current = "";
	let quote: '"' | "'" | undefined;
	let escaped = false;

	for (const char of input) {
		if (escaped) {
			current += char;
			escaped = false;
			continue;
		}
		if (char === "\\" && quote !== "'") {
			escaped = true;
			continue;
		}
		if (quote) {
			if (char === quote) {
				quote = undefined;
			} else {
				current += char;
			}
			continue;
		}
		if (char === '"' || char === "'") {
			quote = char;
			continue;
		}
		if (/\s/.test(char)) {
			if (current) {
				tokens.push(current);
				current = "";
			}
			continue;
		}
		current += char;
	}

	if (escaped) {
		current += "\\";
	}
	if (current) {
		tokens.push(current);
	}
	return tokens;
}

function shellJoin(args: string[]): string {
	return args.map(shellQuote).join(" ");
}

function shellQuote(arg: string): string {
	if (arg === "") {
		return "''";
	}
	if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(arg)) {
		return arg;
	}
	return `'${arg.replace(/'/g, `'\\''`)}'`;
}

function resolveRevdiffBin(): string | undefined {
	const fromEnv = process.env.REVDIFF_BIN;
	if (fromEnv && existsSync(fromEnv)) {
		return fromEnv;
	}
	const local = path.join(REPO_ROOT, ".bin", "revdiff");
	if (existsSync(local)) {
		return local;
	}
	return findInPath("revdiff");
}

function findInPath(binary: string): string | undefined {
	for (const dir of (process.env.PATH ?? "").split(path.delimiter)) {
		if (!dir) {
			continue;
		}
		const candidate = path.join(dir, binary);
		if (existsSync(candidate)) {
			return candidate;
		}
	}
	return undefined;
}

function detectSmartRef(cwd: string): SmartDetectResult | undefined {
	return runDetectRefScript(cwd) ?? detectSmartRefFallback(cwd);
}

function runDetectRefScript(cwd: string): SmartDetectResult | undefined {
	if (!existsSync(DETECT_REF_SCRIPT)) {
		return undefined;
	}
	const result = spawnSync(DETECT_REF_SCRIPT, [], { cwd, encoding: "utf8" });
	if ((result.status ?? 1) !== 0) {
		return undefined;
	}

	const fields = new Map<string, string>();
	for (const line of (result.stdout ?? "").split("\n")) {
		const idx = line.indexOf(":");
		if (idx === -1) {
			continue;
		}
		fields.set(line.slice(0, idx).trim(), line.slice(idx + 1).trim());
	}
	if (fields.size === 0) {
		return undefined;
	}

	return {
		branch: fields.get("branch") ?? "HEAD",
		mainBranch: fields.get("main_branch") ?? "",
		isMain: fields.get("is_main") === "true",
		hasUncommitted: fields.get("has_uncommitted") === "true",
		useStaged: fields.get("use_staged") === "true",
		suggestedRef: fields.get("suggested_ref") ?? "",
		needsAsk: fields.get("needs_ask") === "true",
	};
}

function detectSmartRefFallback(cwd: string): SmartDetectResult | undefined {
	if (gitStdout(["rev-parse", "--is-inside-work-tree"], cwd) !== "true") {
		return undefined;
	}

	// detect no-commits state (fresh repo after git init)
	const hasCommits = gitOk(["rev-parse", "HEAD"], cwd);
	const hasUncommitted = gitStdout(["status", "--porcelain"], cwd).trim().length > 0;
	const hasUnstaged = !gitOk(["diff", "--quiet"], cwd);
	const hasStaged = !gitOk(["diff", "--cached", "--quiet"], cwd);
	const useStaged = hasUncommitted && hasStaged && !hasUnstaged;

	if (!hasCommits) {
		return {
			branch: "HEAD",
			mainBranch: "",
			isMain: false,
			hasUncommitted,
			useStaged: false,
			suggestedRef: "--all-files",
			needsAsk: false,
		};
	}

	const branch = gitStdout(["rev-parse", "--abbrev-ref", "HEAD"], cwd) || "HEAD";
	const mainBranch = detectMainBranch(cwd);
	const isMain = Boolean(mainBranch) && branch === mainBranch;
	let suggestedRef = "";
	let needsAsk = false;

	if (isMain) {
		suggestedRef = hasUncommitted ? "" : "HEAD~1";
	} else if (hasUncommitted) {
		needsAsk = true;
	} else {
		suggestedRef = mainBranch;
	}

	return { branch, mainBranch, isMain, hasUncommitted, useStaged, suggestedRef, needsAsk };
}

function detectMainBranch(cwd: string): string {
	const remoteHead = gitStdout(["symbolic-ref", "refs/remotes/origin/HEAD"], cwd);
	if (remoteHead.startsWith("refs/remotes/origin/")) {
		return remoteHead.slice("refs/remotes/origin/".length);
	}
	if (gitOk(["show-ref", "--verify", "--quiet", "refs/heads/master"], cwd)) {
		return "master";
	}
	if (gitOk(["show-ref", "--verify", "--quiet", "refs/heads/main"], cwd)) {
		return "main";
	}
	return "";
}

function gitStdout(args: string[], cwd: string): string {
	const result = spawnSync("git", args, { cwd, encoding: "utf8" });
	if ((result.status ?? 1) !== 0) {
		return "";
	}
	return (result.stdout ?? "").trim();
}

function gitOk(args: string[], cwd: string): boolean {
	return (spawnSync("git", args, { cwd, stdio: "ignore" }).status ?? 1) === 0;
}
