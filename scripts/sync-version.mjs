#!/usr/bin/env node
// Syncs the Lupinus version number from its single source of truth,
// internal/version/version.go, into every other file that needs to carry
// it (wails.json, frontend/package.json). Run this after editing the
// Version constant, before tagging a release:
//
//   node scripts/sync-version.mjs
//
// Zero dependencies — everything here is Node 20+ builtins. Exits non-zero
// on any read/parse failure so CI fails loudly instead of silently
// shipping a build with a mismatched version.

import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const versionGoPath = path.join(root, "internal", "version", "version.go");
const wailsJsonPath = path.join(root, "wails.json");
const frontendPkgPath = path.join(root, "frontend", "package.json");

let changed = false;

function fail(message) {
	console.error(`sync-version: ${message}`);
	process.exit(1);
}

// --- 1. Read the source of truth ---

let versionGoSrc;
try {
	versionGoSrc = readFileSync(versionGoPath, "utf8");
} catch (err) {
	fail(`could not read ${path.relative(root, versionGoPath)}: ${err.message}`);
}

const match = versionGoSrc.match(/Version\s*=\s*"(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)"/);
if (!match) {
	fail(
		`could not find a Version = "X.Y.Z" constant in ${path.relative(root, versionGoPath)}`,
	);
}
const version = match[1];

// --- 2. wails.json: info.productVersion ---

let wailsJsonRaw;
try {
	wailsJsonRaw = readFileSync(wailsJsonPath, "utf8");
} catch (err) {
	fail(`could not read ${path.relative(root, wailsJsonPath)}: ${err.message}`);
}

let wailsJson;
try {
	wailsJson = JSON.parse(wailsJsonRaw);
} catch (err) {
	fail(`could not parse ${path.relative(root, wailsJsonPath)}: ${err.message}`);
}

if (!wailsJson.info || typeof wailsJson.info !== "object") {
	fail(`${path.relative(root, wailsJsonPath)} has no "info" object`);
}

if (wailsJson.info.productVersion !== version) {
	const from = wailsJson.info.productVersion;
	wailsJson.info.productVersion = version;
	// Preserve trailing newline style / tab indentation used by wails.json.
	writeFileSync(wailsJsonPath, JSON.stringify(wailsJson, null, 2) + "\n");
	console.log(
		`sync-version: wails.json info.productVersion: ${from ?? "(missing)"} -> ${version}`,
	);
	changed = true;
} else {
	console.log(`sync-version: wails.json already in sync (${version})`);
}

// --- 3. frontend/package.json: version (may not exist yet) ---

if (existsSync(frontendPkgPath)) {
	let pkgRaw;
	try {
		pkgRaw = readFileSync(frontendPkgPath, "utf8");
	} catch (err) {
		fail(`could not read ${path.relative(root, frontendPkgPath)}: ${err.message}`);
	}

	let pkg;
	try {
		pkg = JSON.parse(pkgRaw);
	} catch (err) {
		fail(`could not parse ${path.relative(root, frontendPkgPath)}: ${err.message}`);
	}

	if (pkg.version !== version) {
		const from = pkg.version;
		pkg.version = version;
		writeFileSync(frontendPkgPath, JSON.stringify(pkg, null, 2) + "\n");
		console.log(
			`sync-version: frontend/package.json version: ${from ?? "(missing)"} -> ${version}`,
		);
		changed = true;
	} else {
		console.log(`sync-version: frontend/package.json already in sync (${version})`);
	}
} else {
	console.log(
		`sync-version: frontend/package.json does not exist yet, skipping`,
	);
}

if (!changed) {
	console.log(`sync-version: everything already in sync at ${version}`);
}
