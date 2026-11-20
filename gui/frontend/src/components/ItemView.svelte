<script lang="ts">
  import type { Item } from "../lib/types";
  import { clock, elapsed, humanTokens } from "../lib/format";
  import Markdown from "./Markdown.svelte";
  import ToolCard from "./ToolCard.svelte";

  let {
    item,
    cwd,
    showThinking,
    expandAll,
  }: { item: Item; cwd: string; showThinking: boolean; expandAll: boolean } = $props();

  let thinkingOpen: boolean | null = $state(null);

  // thinkingLabel is "Thinking 14s" while the model thinks and "Thought
  // for 15s · 14:10:22" (when it finished) afterwards.
  function thinkingLabel(it: Extract<Item, { kind: "assistant" }>, now: number): string {
    if (it.thinkEnd) return `Thought for ${elapsed(it.thinkEnd - it.thinkStart)} · ${clock(it.thinkEnd)}`;
    if (it.thinkStart && it.streaming) return `Thinking ${elapsed(now - it.thinkStart)}`;
    return "Thinking";
  }

  // now ticks while a message streams, for the running times.
  let now = $state(Date.now());
  $effect(() => {
    if (item.kind !== "assistant" || !item.streaming) return;
    now = Date.now();
    const t = setInterval(() => (now = Date.now()), 500);
    return () => clearInterval(t);
  });
</script>

{#if item.kind === "user"}
  <div class="user">
    <div class="text">{item.text}</div>
    {#if item.attached.length}
      <div class="attached">Attached: {item.attached.join(", ")}</div>
    {/if}
  </div>
{:else if item.kind === "assistant"}
  <div class="assistant">
    {#if item.thinking}
      <details class="thinking" open={thinkingOpen ?? (showThinking || (item.streaming && !item.text))}>
        <summary
          onclick={(e) => {
            e.preventDefault();
            thinkingOpen = !(thinkingOpen ?? (showThinking || (item.streaming && !item.text)));
          }}
          >{thinkingLabel(item, now)}{#if item.streaming && item.thinkStart && !item.thinkEnd}<span class="spinner"
            ></span>{/if}</summary
        >
        <div class="thinking-text">{item.thinking}</div>
      </details>
    {/if}
    {#if item.text}
      <Markdown src={item.text} streaming={item.streaming} />
    {/if}
    {#if item.composing}
      <div class="muted">Preparing {item.composing}…</div>
    {:else if item.streaming && !item.text && !item.thinking}
      {#if item.requested}
        <div class="muted"><span class="spinner"></span> Waiting for the model · {elapsed(now - item.requested)}</div>
      {:else}
        <div class="muted pulse">Working…</div>
      {/if}
    {/if}
    {#if item.cancelled}
      <div class="muted">(cancelled)</div>
    {/if}
  </div>
{:else if item.kind === "tool"}
  <ToolCard tool={item.tool} {cwd} {expandAll} />
{:else if item.kind === "compaction"}
  <details class="compaction">
    <summary>
      {#if item.running}
        Compacting the conversation ({humanTokens(item.before)} tokens)…
      {:else if item.error}
        Compaction failed: {item.error}
      {:else}
        Compacted the conversation{item.before ? ` from ${humanTokens(item.before)} tokens` : ""}{item.after
          ? ` to ${humanTokens(item.after)}`
          : ""}
      {/if}
    </summary>
    {#if item.summary}
      <Markdown src={item.summary} />
    {/if}
  </details>
{:else if item.kind === "notice"}
  <div class="notice {item.level}">{item.text}</div>
{:else if item.kind === "text"}
  <div class="textitem">
    <div class="title">{item.title}</div>
    <Markdown src={item.body} />
  </div>
{/if}

<style>
  .user {
    align-self: flex-end;
    max-width: 85%;
    background: var(--user-bg);
    border-radius: 12px;
    padding: 8px 12px;
  }
  .user .text {
    white-space: pre-wrap;
    word-break: break-word;
  }
  .attached,
  .muted {
    color: var(--fg-muted);
    font-size: 12.5px;
  }
  .assistant {
    padding: 0 2px;
  }
  .thinking {
    color: var(--fg-muted);
    margin-bottom: 6px;
  }
  .thinking summary,
  .compaction summary {
    cursor: pointer;
    font-size: 12.5px;
    user-select: none;
  }
  .thinking-text {
    white-space: pre-wrap;
    font-style: italic;
    border-left: 2px solid var(--border);
    padding-left: 10px;
    margin-top: 4px;
  }
  .compaction {
    border: 1px dashed var(--border);
    border-radius: var(--radius);
    padding: 6px 10px;
    color: var(--fg-muted);
  }
  .notice {
    font-size: 13px;
    padding: 4px 10px;
    border-left: 3px solid var(--info);
    color: var(--fg-muted);
    white-space: pre-wrap;
  }
  .notice.warn {
    border-color: var(--warn);
  }
  .notice.error {
    border-color: var(--err);
    color: var(--err);
  }
  .notice.banner {
    border: 1px solid var(--warn);
    border-radius: var(--radius);
    color: var(--warn);
    font-weight: 600;
  }
  .textitem {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 10px 14px;
    background: var(--bg-soft);
  }
  .title {
    font-weight: 600;
    margin-bottom: 6px;
    color: var(--accent);
  }
  .spinner {
    display: inline-block;
    width: 0.8em;
    height: 0.8em;
    margin-left: 0.2em;
    vertical-align: -0.1em;
    border: 2px solid currentColor;
    border-right-color: transparent;
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .pulse {
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.4;
    }
  }
</style>
