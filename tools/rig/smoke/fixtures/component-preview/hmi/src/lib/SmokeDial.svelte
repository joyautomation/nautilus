<script lang="ts">
  // A smoke-test component for check 20 (the component preview): its
  // label and value are what the check reads back from the pane's DOM.
  // Space-indented on purpose: the check edits it through VS Code's Go to
  // Line (line:col), whose column is visual, so a tab would put the cursor
  // somewhere else than the byte column the check computed.
  let {
    label = 'dial',
    value = 42,
    color = '#3987e5',
    width = 160
  }: { label?: string; value?: number; color?: string; width?: number } = $props();
</script>

<svg viewBox="0 0 160 100" {width} role="img" aria-label="smoke dial {label}">
  <rect x="2" y="2" width="156" height="96" rx="8" fill="none" stroke={color} stroke-width="3" />
  <text x="80" y="44" text-anchor="middle" data-role="label" font-size="16" fill="currentColor">{label}</text>
  <text x="80" y="78" text-anchor="middle" data-role="value" font-size="22" fill={color}>{value}</text>
</svg>
