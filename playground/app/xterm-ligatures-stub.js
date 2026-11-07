// @xterm/addon-ligatures reads system fonts through Node APIs; the
// workbench only offers it, and font ligatures in a terminal do not matter
// in the playground. An inert add-on in its place (vite.config.js).
export class LigaturesAddon {
	activate() {}
	dispose() {}
}
