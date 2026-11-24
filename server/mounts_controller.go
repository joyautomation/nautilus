package server

import "net/http"

// The controller-only route groups (mount.go). New mounts all of them;
// NewPlayground mounts none, so the browser playground's wasm links none of
// their handlers. Each group is a plain function, so a group nobody names is
// dropped by the linker; nothing at package level may name one (an init or a
// package variable would keep every handler reachable).
//
// A route keeps exactly the wrapping it had in Handler: s.audited(...) for a
// write, and the authorize and refusal rows inside the handler. Moving a
// registration changes where it is written, not what answers it.

// controllerMounts is every group a controller serves, in the order Handler
// adds them.
func controllerMounts() []Mount {
	return []Mount{mountAuth, mountPush, mountAckLinks, mountCluster, mountDisk, mountIOLink, mountHistory}
}

// mountAuth: sign-in and sign-out, elevation, and the OpenID Connect hop. /api/me and /api/audit stay core: the kit reads who the caller is, and the audit trail, on every controller.
func mountAuth(s *Server, mux *http.ServeMux) {
	// Sign-in and sign-out journal themselves (auth.signin, auth.signout),
	// outside the audited seam: TestEveryWriteRouteIsAudited names them.
	mux.HandleFunc("GET /signin", s.handleSignInPage)
	mux.HandleFunc("POST /api/signin", s.handleSignIn)
	mux.HandleFunc("POST /api/signout", s.handleSignOut)
	mux.HandleFunc("POST /api/elevate", s.handleElevate)
	mux.HandleFunc("GET /auth/oidc/start", s.handleOIDCStart)
	mux.HandleFunc("GET /auth/oidc/callback", s.handleOIDCCallback)
	mux.HandleFunc("GET /auth/oidc/elevated", s.handleOIDCElevated)
}

// mountPush: Web Push: the key, a person's subscriptions and their health, subscribing and unsubscribing.
func mountPush(s *Server, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/push/key", s.handlePushKey)
	mux.HandleFunc("GET /api/push/subscriptions", s.handlePushList)
	mux.HandleFunc("GET /api/push/health", s.handlePushHealth)
	mux.Handle("POST /api/push/subscribe", s.audited("push.subscribe", pushTarget, s.handlePushSubscribe))
	mux.Handle("POST /api/push/confirm-revocations", s.audited("push.confirm-revocations", pushConfirmTarget, s.handlePushConfirm))
	mux.Handle("DELETE /api/push/subscribe", s.audited("push.unsubscribe", pushTarget, s.handlePushUnsubscribe))
}

// mountAckLinks: acknowledgement links and the SMS/voice providers' inbound replies (#715).
func mountAckLinks(s *Server, mux *http.ServeMux) {
	// Ack links (#715): GET is the confirm page and changes nothing (a
	// mail scanner that opens links acks nothing); POST acknowledges.
	// The paths are acklink.Path (TestAckLinkRoutesAreAcklinkPath).
	mux.HandleFunc("GET /alarm-ack/{token}", s.handleAckLinkPage)
	mux.Handle("POST /alarm-ack/{token}", s.audited("alarm.ack", linkAuditTarget, s.handleAckLink, s.checkLink))
	// Inbound acknowledgements (#715): the SMS and voice provider posts a
	// reply's code or a call's keypress, signed (InboundSMSPath,
	// InboundVoicePath; TestInboundRoutesAreTheirPaths).
	mux.Handle("POST /alarm-inbound/sms", s.audited("alarm.ack", inboundAuditTarget, s.handleInboundAck, s.checkInbound("sms")))
	mux.Handle("POST /alarm-inbound/voice/{token}", s.audited("alarm.ack", inboundAuditTarget, s.handleInboundAck, s.checkInbound("voice")))
}

// mountCluster: the redundant pair's operator commands. GET /api/cluster (who leads) stays core: the dashboard shows it.
func mountCluster(s *Server, mux *http.ServeMux) {
	mux.Handle("POST /api/cluster/clear-duplicate", s.audited("redundancy.clear-duplicate", s.clearDuplicateTarget, s.handleClearDuplicate))
	mux.Handle("POST /api/cluster/commission", s.audited("redundancy.commission", s.commissionTarget, s.handleCommission))
	mux.Handle("POST /api/cluster/recommission", s.audited("redundancy.recommission", s.recommissionTarget, s.handleRecommission))
	mux.Handle("POST /api/cluster/vouch", s.audited("redundancy.vouch", s.vouchTarget, s.handleVouch))
	mux.Handle("POST /api/cluster/release-hold", s.audited("redundancy.release-hold", s.releaseHoldTarget, s.handleReleaseHold))
	mux.Handle("POST /api/cluster/switchover", s.audited("redundancy.switchover", s.switchoverTarget, s.handleSwitchover))
}

// mountDisk: what reads or writes a controller's files: the live I/O configuration and its staged edits, config files and commits, the program's commit history and activating one, and importing parameters.
func mountDisk(s *Server, mux *http.ServeMux) {
	mux.Handle("POST /api/parameters/import", s.audited("param.import", s.paramImportTarget, s.handleParametersImport))
	mux.HandleFunc("GET /api/program/history", s.handleProgramHistory)
	mux.Handle("POST /api/program/activate", s.audited("program.activate", s.activateTarget, s.handleActivate))
	mux.HandleFunc("GET /api/io", s.handleIO)
	mux.HandleFunc("GET /api/io/files", s.handleIOFiles)
	mux.HandleFunc("GET /api/config/files", s.handleConfigFiles)
	mux.Handle("POST /api/io/staged", s.audited("config.stage", ioStageTarget, s.handleIOStage))
	mux.HandleFunc("GET /api/io/staged/{id}", s.handleIOStaged)
	mux.Handle("POST /api/io/staged/{id}/validate", s.audited("config.validate", ioStagedTarget, s.handleIOValidate))
	mux.Handle("POST /api/io/staged/{id}/apply", s.audited("config.apply", ioApplyTarget, s.handleIOApply))
	mux.HandleFunc("GET /api/config/patch", s.handleConfigPatch)
	mux.Handle("POST /api/config/commit", s.audited("config.commit", configCommitTarget, s.handleConfigCommit))
	mux.Handle("POST /api/io/revert/cancel", s.audited("config.revert.cancel", nil, s.handleIOCancelRevert))
}

// mountIOLink: IO-Link port devices' parameters.
func mountIOLink(s *Server, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/iolink/{port}/params", s.handleParamList)
	mux.HandleFunc("GET /api/iolink/{port}/params/{ref}", s.handleParamRead)
	mux.Handle("PUT /api/iolink/{port}/params/{ref}", s.audited("param.write", s.paramTarget, s.handleParamWrite))
}

// mountHistory: the historian proxy.
func mountHistory(s *Server, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/history/", s.handleHistory)
}
