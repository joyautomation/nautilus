package uaserver

import (
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
)

// DiscoveryService implements the Discovery Service Set
//
// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.4
type DiscoveryService struct {
	srv *Server
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.4.3
func (s *DiscoveryService) FindServersOnNetwork(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.FindServersOnNetworkRequest](r)
	if err != nil {
		return nil, err
	}
	return serviceUnsupported(req.RequestHeader), nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.4.5
func (s *DiscoveryService) RegisterServer(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.RegisterServerRequest](r)
	if err != nil {
		return nil, err
	}
	return serviceUnsupported(req.RequestHeader), nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.4.6
func (s *DiscoveryService) RegisterServer2(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.RegisterServer2Request](r)
	if err != nil {
		return nil, err
	}
	return serviceUnsupported(req.RequestHeader), nil
}
