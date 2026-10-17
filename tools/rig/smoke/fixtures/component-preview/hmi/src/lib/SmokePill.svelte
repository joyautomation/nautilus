<script lang="ts">
  // The second component of check 20: what the pane follows to when the
  // active editor moves to another .svelte.
  let { text = 'pill', width = 120 }: { text?: string; width?: number } = $props();
</script>

<svg viewBox="0 0 120 40" {width} role="img" aria-label="smoke pill {text}">
  <rect x="2" y="2" width="116" height="36" rx="18" fill="none" stroke="#5fb98c" stroke-width="3" />
  <text x="60" y="26" text-anchor="middle" data-role="pill" font-size="16" fill="currentColor">{text}</text>
</svg>
