<script lang="ts">
	// The plant's shift log (#720): the notes written for whoever works next,
	// newest day first, handovers on top of their day. It is `NotesPanel` on
	// the `log` target in the shift log's layout.
	import NotesPanel from './NotesPanel.svelte';
	import { NotesClient, DraftKeeper } from '../notes.js';

	let {
		client = new NotesClient(),
		drafts = new DraftKeeper(),
		pollMs = 15000
	}: {
		client?: NotesClient;
		drafts?: DraftKeeper;
		pollMs?: number;
	} = $props();
</script>

<NotesPanel {client} {drafts} target={{ kind: 'log' }} heading="Shift log" grouped {pollMs} />
