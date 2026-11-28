// Helpers for the settings view, which edits Rho's config files.

export type Tree = Record<string, unknown>;

/** Returns the value at a dotted key ("bash.timeout"), or undefined. */
export function get(tree: unknown, key: string): unknown {
  let cur: unknown = tree;
  for (const k of key.split(".")) {
    if (!cur || typeof cur !== "object" || Array.isArray(cur)) return undefined;
    cur = (cur as Tree)[k];
  }
  return cur;
}

/** A provider's entry in a config tree, or undefined. */
export function providerIn(tree: unknown, name: string): Tree | undefined {
  const p = get(tree, "providers") as Tree | undefined;
  const v = p?.[name];
  return v && typeof v === "object" ? (v as Tree) : undefined;
}

/** The models listed under a provider in a config tree. */
export function modelsIn(tree: unknown, name: string): Tree[] {
  const m = providerIn(tree, name)?.models;
  return Array.isArray(m) ? (m as Tree[]) : [];
}

/** Provider names in a config tree, sorted. */
export function providerNames(tree: unknown): string[] {
  return Object.keys((get(tree, "providers") as Tree) ?? {}).sort();
}

/** Copies the keys of fields that obj sets. */
export function pick(obj: Tree | undefined, keys: string[]): Tree {
  const out: Tree = {};
  for (const k of keys) if (obj && obj[k] !== undefined && obj[k] !== null) out[k] = structuredClone(obj[k]);
  return out;
}

/** Shows a value in a sentence. */
export function shown(v: unknown): string {
  if (v === undefined || v === null || v === "") return "unset";
  if (Array.isArray(v)) return v.length ? v.join(", ") : "none";
  if (typeof v === "boolean") return v ? "on" : "off";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}
