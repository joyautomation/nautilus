// The view state every model reads off context (`hmi3d:view`): SceneView
// owns a $state object and every kind reads it, the way the palette is
// shared. `lit` is the §3c look — PBR values, textures and the environment
// on — and `false` is the Milestone 1 flat look.
export interface ViewState {
	lit: boolean;
}
