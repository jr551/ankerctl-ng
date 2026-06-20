package service

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/django1982/ankerctl/internal/config"
	"github.com/django1982/ankerctl/internal/db"
	"github.com/django1982/ankerctl/internal/logging"
	"github.com/django1982/ankerctl/internal/model"
	ppppclient "github.com/django1982/ankerctl/internal/pppp/client"
	"github.com/django1982/ankerctl/internal/pppp/protocol"
	"github.com/django1982/ankerctl/internal/util"
	"github.com/google/uuid"
)

type ppppConn interface {
	ConnectLANSearch() error
	Run(ctx context.Context) error
	Close() error
	State() ppppclient.State
	Healthy() bool
	Channel(index int) (*protocol.Channel, error)
	RemoteIP() net.IP
}

type ppppClientFactory func(ctx context.Context) (ppppConn, error)

// PPPPService manages the LAN PPPP connection and XZYH dispatch.
type PPPPService struct {
	BaseWorker

	log          *slog.Logger
	client       ppppConn
	clientMu     sync.Mutex
	clientFactor ppppClientFactory
	pollInterval time.Duration

	// powerController, if set, allows Upload to power-cycle the printer
	// when the PPPP session is persistently stuck (printer accepts
	// handshake but immediately drops the session).
	powerController PrinterPowerController

	// cfgMgr, database, and printerIndex are used to persist the discovered
	// printer IP back to default.json and the DB cache on every successful
	// LAN connection. printerIndex is resolved dynamically so it works even
	// when the service is created before the user logs in.
	cfgMgr       *config.Manager
	database     *db.DB
	printerIndex int

	handlersMu    sync.RWMutex
	handlers      map[byte][]func([]byte)
	videoHandlers []func(protocol.VideoFrame)
	aabbHandlers  map[byte][]func(protocol.Aabb, []byte)
}

// NewPPPPService creates a PPPP service.
func NewPPPPService(cfg *config.Manager, printerIndex int) *PPPPService {
	return NewPPPPServiceWithDB(cfg, printerIndex, nil)
}

// WithPowerController injects a power controller so Upload can power-cycle
// the printer to recover from stuck PPPP sessions. Call before Start().
func (s *PPPPService) WithPowerController(pc PrinterPowerController) *PPPPService {
	s.powerController = pc
	return s
}

// NewPPPPServiceWithDB creates a PPPP service that consults a DB cache for
// the last-known printer IP before falling back to a LAN broadcast.
func NewPPPPServiceWithDB(cfg *config.Manager, printerIndex int, database *db.DB) *PPPPService {
	s := &PPPPService{
		BaseWorker:   NewBaseWorker("ppppservice"),
		log:          slog.With("service", "ppppservice"),
		pollInterval: 50 * time.Millisecond,
		handlers:     make(map[byte][]func([]byte)),
		aabbHandlers: make(map[byte][]func(protocol.Aabb, []byte)),
		cfgMgr:       cfg,
		database:     database,
		printerIndex: printerIndex,
	}
	s.clientFactor = defaultPPPPClientFactory(cfg, printerIndex, database)
	s.BindHooks(s)
	return s
}

func defaultPPPPClientFactory(cfgMgr *config.Manager, printerIndex int, database *db.DB) ppppClientFactory {
	return func(ctx context.Context) (ppppConn, error) {
		if cfgMgr == nil {
			return nil, errors.New("ppppservice: config manager is nil")
		}
		cfg, err := cfgMgr.Load()
		if err != nil {
			return nil, fmt.Errorf("ppppservice: load config: %w", err)
		}
		if cfg == nil || len(cfg.Printers) == 0 {
			return nil, errors.New("ppppservice: no printers configured")
		}
		if printerIndex < 0 || printerIndex >= len(cfg.Printers) {
			return nil, fmt.Errorf("ppppservice: printer index out of range: %d", printerIndex)
		}

		printer := cfg.Printers[printerIndex]
		duid, err := protocol.ParseDuidString(printer.P2PDUID)
		if err != nil {
			return nil, fmt.Errorf("ppppservice: parse p2p duid: %w", err)
		}

		// Prefer a directed broadcast derived from the printer subnet when we
		// know the printer IP. On multi-homed hosts, 255.255.255.255 can leave
		// through the wrong interface and never reach the printer VLAN.
		knownIP := strings.TrimSpace(printer.IPAddr)
		if knownIP == "" && database != nil && printer.SN != "" {
			if cachedIP, dbErr := database.GetPrinterIP(printer.SN); dbErr == nil && cachedIP != "" {
				knownIP = strings.TrimSpace(cachedIP)
				slog.Info("ppppservice: known cached IP for handshake", "ip", knownIP, "sn", printer.SN)
			}
		}
		if knownIP != "" {
			slog.Info("ppppservice: known IP for handshake", "ip", knownIP, "duid", logging.RedactID(printer.P2PDUID, 4))
		}

		cli, err := openHandshakePPPPClient(duid, knownIP)
		if err != nil {
			return nil, fmt.Errorf("ppppservice: open broadcast lan client: %w", err)
		}
		if err := cli.ConnectLANSearch(); err != nil {
			_ = cli.Close()
			return nil, fmt.Errorf("ppppservice: connect lan search: %w", err)
		}
		slog.Info("ppppservice: LanSearch sent, awaiting PunchPkt", "duid", logging.RedactID(printer.P2PDUID, 4))
		return cli, nil
	}
}

func openHandshakePPPPClient(duid protocol.Duid, knownIP string) (*ppppclient.Client, error) {
	if targets := handshakeAddressesForKnownIP(knownIP); len(targets) > 0 {
		targetStrings := make([]string, 0, len(targets))
		for _, target := range targets {
			targetStrings = append(targetStrings, target.String())
		}
		slog.Info("ppppservice: using known printer IP handshake targets", "targets", strings.Join(targetStrings, ","))
		return ppppclient.OpenBroadcastLANToMany(duid, targets)
	}
	return ppppclient.OpenBroadcastLAN(duid)
}

func handshakeAddressForKnownIP(knownIP string) (net.IP, bool) {
	targets := handshakeAddressesForKnownIP(knownIP)
	if len(targets) == 0 {
		return nil, false
	}
	return targets[0], true
}

func handshakeAddressesForKnownIP(knownIP string) []net.IP {
	ip, ok := handshakeTargetForKnownIP(knownIP)
	if !ok {
		return nil
	}

	targets := []net.IP{ip}
	if broadcast := classCBroadcastForTarget(ip); broadcast != nil {
		targets = append(targets, broadcast)
	}
	if directed, ok := directedBroadcastForTarget(ip); ok {
		targets = append(targets, directed)
	}
	targets = append(targets, net.IPv4bcast)
	return uniqueIPv4s(targets)
}

func handshakeTargetForKnownIP(knownIP string) (net.IP, bool) {
	ip := net.ParseIP(strings.TrimSpace(knownIP))
	if !util.IsValidPrinterIP(ip) {
		return nil, false
	}
	return ip.To4(), true
}

func classCBroadcastForTarget(target net.IP) net.IP {
	ip := target.To4()
	if ip == nil {
		return nil
	}
	return net.IPv4(ip[0], ip[1], ip[2], 255)
}

func uniqueIPv4s(ips []net.IP) []net.IP {
	seen := make(map[string]bool, len(ips))
	out := make([]net.IP, 0, len(ips))
	for _, raw := range ips {
		ip := raw.To4()
		if ip == nil {
			continue
		}
		key := ip.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ip)
	}
	return out
}

func directedBroadcastForTarget(target net.IP) (net.IP, bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, false
	}
	return directedBroadcastForTargetWithInterfaces(target, ifaces, func(iface net.Interface) ([]net.Addr, error) {
		return iface.Addrs()
	})
}

func directedBroadcastForTargetWithInterfaces(target net.IP, ifaces []net.Interface, addrsFn func(net.Interface) ([]net.Addr, error)) (net.IP, bool) {
	target = target.To4()
	if target == nil {
		return nil, false
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := addrsFn(iface)
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet == nil {
				continue
			}
			localIP := ipnet.IP.To4()
			mask := ipnet.Mask
			if len(mask) == net.IPv6len {
				mask = mask[12:]
			}
			if localIP == nil || len(mask) != net.IPv4len {
				continue
			}
			if !ipnet.Contains(target) {
				continue
			}
			broadcast := make(net.IP, net.IPv4len)
			for i := 0; i < net.IPv4len; i++ {
				broadcast[i] = (localIP[i] & mask[i]) | (^mask[i])
			}
			return broadcast, true
		}
	}
	return nil, false
}

// RegisterXzyhHandler registers a handler for XZYH frames on the given channel.
func (s *PPPPService) RegisterXzyhHandler(channel byte, fn func([]byte)) {
	if fn == nil {
		return
	}
	s.handlersMu.Lock()
	s.handlers[channel] = append(s.handlers[channel], fn)
	s.handlersMu.Unlock()
}

// RegisterVideoHandler registers a handler for 64-byte video frames (channel 1).
func (s *PPPPService) RegisterVideoHandler(fn func(protocol.VideoFrame)) {
	if fn == nil {
		return
	}
	s.handlersMu.Lock()
	s.videoHandlers = append(s.videoHandlers, fn)
	s.handlersMu.Unlock()
}

// RegisterAabbHandler registers a handler for AABB frames on the given channel.
func (s *PPPPService) RegisterAabbHandler(channel byte, fn func(protocol.Aabb, []byte)) {
	if fn == nil {
		return
	}
	s.handlersMu.Lock()
	s.aabbHandlers[channel] = append(s.aabbHandlers[channel], fn)
	s.handlersMu.Unlock()
}

// P2PCommand sends a JSON-wrapped P2P command on channel 0.
func (s *PPPPService) P2PCommand(ctx context.Context, subCmd protocol.P2PSubCmdType, payload any) error {
	cli := s.currentClient()
	if cli == nil {
		return errors.New("ppppservice: no client")
	}
	ch, err := cli.Channel(0)
	if err != nil {
		return err
	}

	data := map[string]any{
		"commandType": int(subCmd),
	}
	if payload != nil {
		data["data"] = payload
	}
	jb, err := json.Marshal(data)
	if err != nil {
		return err
	}

	x := protocol.Xzyh{
		Cmd:  protocol.P2PCmdP2pJson,
		Chan: 0,
		Data: jb,
	}
	xb, err := x.MarshalBinary()
	if err != nil {
		return err
	}

	_, _, err = ch.Write(xb, false)
	return err
}

// StartLive starts the video stream from the printer.
func (s *PPPPService) StartLive(ctx context.Context, mode int) error {
	// mode is currently ignored by the start_live call in Python, but used in SetVideoMode
	return s.P2PCommand(ctx, protocol.P2PSubCmdStartLive, map[string]any{
		"encryptkey": "x",
		"accountId":  "y",
	})
}

// StopLive stops the video stream.
func (s *PPPPService) StopLive(ctx context.Context) error {
	return s.P2PCommand(ctx, protocol.P2PSubCmdCloseLive, nil)
}

// SetVideoMode switches stream resolution/quality.
func (s *PPPPService) SetVideoMode(ctx context.Context, mode int) error {
	return s.P2PCommand(ctx, protocol.P2PSubCmdLiveModeSet, map[string]any{"mode": mode})
}

// SetLight toggles the printer camera light.
func (s *PPPPService) SetLight(ctx context.Context, on bool) error {
	return s.P2PCommand(ctx, protocol.P2PSubCmdLightStateSwitch, map[string]any{"open": on})
}

// waitConnected blocks until the PPPP client reaches StateConnected AND is
// Healthy (recent PingResp within ppppStaleThreshold) or the context / a
// 10-second deadline expires. This mirrors Python's pppp_open() which polls
// until api.state == PPPPState.Connected before uploading.
//
// The Healthy() guard is critical: the printer's Wi-Fi power saving can drop
// a session without sending Close, leaving State()==StateConnected while no
// traffic flows. Without this check, Upload() would send AABB BEGIN and hang
// for the full aabbReplyTimeout on every chunk before failing.
func (s *PPPPService) waitConnected(ctx context.Context) (ppppConn, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		cli := s.currentClient()
		if cli != nil && cli.State() == ppppclient.StateConnected && cli.Healthy() {
			return cli, nil
		}
		if time.Now().After(deadline) {
			// If the client reports Connected but is not Healthy, the session
			// is stale — force a restart so the next attempt gets a fresh
			// handshake instead of reusing the dead socket.
			if cli != nil && cli.State() == ppppclient.StateConnected && !cli.Healthy() {
				s.log.Warn("ppppservice: session stale (no keepalive pong), forcing restart")
				return nil, errStaleSession
			}
			return nil, errors.New("ppppservice: connection timeout waiting for handshake")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// errStaleSession signals that the PPPP session was Connected but not Healthy
// (no recent PingResp). WorkerRun treats this as a restart signal so the next
// Borrow gets a fresh handshake rather than reusing the dead socket.
var errStaleSession = errors.New("ppppservice: stale session (no keepalive pong)")

var aabbReplyTimeout = 15 * time.Second

// Upload implements PPPPFileUploader interface.
// uploadMaxRetries is how many times uploadOnce retries after a connection
// drop before escalating to a power-cycle.
const uploadMaxRetries = 3

// uploadMaxPowerCycles is the maximum number of printer power-cycles
// attempted during a single Upload call. Each power-cycle is followed by
// a 60-second boot window.
const uploadMaxPowerCycles = 3

// uploadTotalTimeout is the hard ceiling for a single Upload call,
// including all retries and power-cycles. The HTTP handler blocks for
// this entire duration, so it must be reasonable for a user waiting on
// a slicer response.
const uploadTotalTimeout = 5 * time.Minute

// uploadRecoveryBootWait is how long we wait after turning the socket
// back on before polling for the printer's PPPP port.
const uploadRecoveryBootWait = 30 * time.Second

// Upload implements PPPPFileUploader interface.
//
// The upload is held in memory (payload bytes) and retried persistently:
//   - Round 1: try uploadOnce with uploadMaxRetries connection-drop retries.
//   - If all fail with connection-level errors, power-cycle the printer.
//   - Wait for the printer to boot and its PPPP daemon to start.
//   - Repeat until the upload succeeds or uploadTotalTimeout expires.
//
// This ensures the file is delivered even when the printer's PPPP stack
// is stuck — the power-cycle clears the stuck state and the fresh boot
// allows a clean session.
func (s *PPPPService) Upload(ctx context.Context, info UploadInfo, payload []byte, progress func(sent, total int64)) error {
	totalDeadline := time.Now().Add(uploadTotalTimeout)
	var lastErr error
	powerCycles := 0

	for {
		err := s.uploadWithRetries(ctx, info, payload, progress)
		if err == nil {
			return nil
		}
		lastErr = err

		if !isRetryableUploadErr(err) {
			return err
		}

		if time.Now().After(totalDeadline) {
			return fmt.Errorf("ppppservice: upload timed out after %v: %w", uploadTotalTimeout, lastErr)
		}

		if s.powerController == nil {
			return fmt.Errorf("ppppservice: upload failed (no power controller for recovery): %w", lastErr)
		}

		if powerCycles >= uploadMaxPowerCycles {
			return fmt.Errorf("ppppservice: upload failed after %d power-cycles: %w", powerCycles, lastErr)
		}

		powerCycles++
		s.log.Warn("ppppservice: printer stuck, power-cycling to recover",
			"cycle", powerCycles, "max", uploadMaxPowerCycles, "err", lastErr)

		pcCtx, pcCancel := context.WithTimeout(context.Background(), 60*time.Second)
		if pcErr := s.powerController.PowerCycle(pcCtx); pcErr != nil {
			s.log.Warn("ppppservice: power-cycle failed", "err", pcErr)
			pcCancel()
			if powerCycles >= uploadMaxPowerCycles {
				return fmt.Errorf("ppppservice: power-cycle failed and no retries left: %w (power-cycle err: %v)", lastErr, pcErr)
			}
			continue
		}
		pcCancel()

		s.log.Info("ppppservice: waiting for printer to boot after power-cycle",
			"boot_wait", uploadRecoveryBootWait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(uploadRecoveryBootWait):
		}

		printerIP := ResolvePrinterIP(s.cfgMgr, s.printerIndex)
		if printerIP == "" {
			s.log.Warn("ppppservice: no printer IP, skipping recovery ping")
			continue
		}

		if err := waitForPrinterRecovery(ctx, printerIP, totalDeadline); err != nil {
			s.log.Warn("ppppservice: printer did not become reachable after power-cycle", "err", err)
		}

		// Force a PPPP service restart so the next uploadWithRetries
		// starts a fresh handshake against the freshly-booted printer.
		s.Restart()
		s.log.Info("ppppservice: printer recovered, retrying upload")
	}
}

// uploadWithRetries calls uploadOnce up to uploadMaxRetries times,
// with a short delay between attempts for the PPPP service to reconnect.
func (s *PPPPService) uploadWithRetries(ctx context.Context, info UploadInfo, payload []byte, progress func(sent, total int64)) error {
	var lastErr error
	for attempt := 0; attempt <= uploadMaxRetries; attempt++ {
		if attempt > 0 {
			s.log.Warn("ppppservice: retrying upload after connection drop",
				"attempt", attempt, "max", uploadMaxRetries, "prev_err", lastErr.Error())
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}

		err := s.uploadOnce(ctx, info, payload, progress)
		if err == nil {
			return nil
		}
		lastErr = err

		if !isRetryableUploadErr(err) {
			return err
		}
	}
	return lastErr
}

// isRetryableUploadErr reports whether an upload error is worth retrying
// (connection dropped, channel closed, stale session, handshake timeout).
func isRetryableUploadErr(err error) bool {
	if errors.Is(err, protocol.ErrChannelClosed) || errors.Is(err, errStaleSession) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "channel closed") ||
		strings.Contains(msg, "connection timeout") ||
		strings.Contains(msg, "no client")
}

func (s *PPPPService) uploadOnce(ctx context.Context, info UploadInfo, payload []byte, progress func(sent, total int64)) error {
	cli, err := s.waitConnected(ctx)
	if err != nil {
		// Stale sessions must trigger a restart so the next attempt gets a
		// fresh handshake rather than reusing the dead socket.
		if errors.Is(err, errStaleSession) {
			s.log.Warn("ppppservice: restarting due to stale session before upload")
			s.Restart()
		}
		return err
	}
	ch, err := cli.Channel(1)
	if err != nil {
		return err
	}

	replyCh := make(chan error, 1)
	handlerIdx := byte(1) // we listen on channel 1

	// Set up temporary reply tap
	s.handlersMu.Lock()
	aabbPos := len(s.aabbHandlers[handlerIdx])
	wrapper := func(aabb protocol.Aabb, data []byte) {
		if len(data) != 1 {
			select {
			case replyCh <- fmt.Errorf("unexpected aabb reply len %d", len(data)):
			default:
			}
			return
		}
		if data[0] != 0 { // 0 = OK
			select {
			case replyCh <- fmt.Errorf("aabb reply error code: %d", data[0]):
			default:
			}
			return
		}
		select {
		case replyCh <- nil:
		default:
		}
	}
	s.aabbHandlers[handlerIdx] = append(s.aabbHandlers[handlerIdx], wrapper)
	s.handlersMu.Unlock()

	defer func() {
		s.handlersMu.Lock()
		handlers := s.aabbHandlers[handlerIdx]
		if aabbPos >= 0 && aabbPos < len(handlers) {
			handlers = append(handlers[:aabbPos], handlers[aabbPos+1:]...)
			s.aabbHandlers[handlerIdx] = handlers
		}
		s.handlersMu.Unlock()
	}()

	waitReply := func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(aabbReplyTimeout):
			return errors.New("timeout waiting for aabb reply")
		case err := <-replyCh:
			return err
		}
	}

	writeXzyh := func(ch *protocol.Channel, cmd protocol.P2PCmdType, chanID byte, data []byte) error {
		x := protocol.Xzyh{
			Cmd:  cmd,
			Chan: chanID,
			Data: data,
		}
		xb, err := x.MarshalBinary()
		if err != nil {
			return err
		}
		if _, _, err := ch.WriteContext(ctx, xb, true); err != nil {
			return err
		}
		return nil
	}

	ch0, err := cli.Channel(0)
	if err != nil {
		return err
	}

	fileUUID := strings.TrimSpace(info.MachineID)
	if fileUUID == "" || fileUUID == "-" {
		fileUUID = strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	}
	legacyID := fileUUID
	if len(legacyID) < 16 {
		legacyID += strings.Repeat("0", 16-len(legacyID))
	}
	legacyID = legacyID[:16]

	// 1. Send XZYH P2P_SEND_FILE (JSON handshake on channel 0, Python parity).
	xzyhReplyCh := make(chan []byte, 1)
	s.handlersMu.Lock()
	xzyhPos := len(s.handlers[0])
	s.handlers[0] = append(s.handlers[0], func(data []byte) {
		select {
		case xzyhReplyCh <- append([]byte(nil), data...):
		default:
		}
	})
	s.handlersMu.Unlock()
	defer func() {
		s.handlersMu.Lock()
		handlers := s.handlers[0]
		if xzyhPos >= 0 && xzyhPos < len(handlers) {
			handlers = append(handlers[:xzyhPos], handlers[xzyhPos+1:]...)
			s.handlers[0] = handlers
		}
		s.handlersMu.Unlock()
	}()

	jsonPayload := map[string]any{
		"uuid":          fileUUID,
		"device":        "ankerctl",
		"flag":          0,
		"random":        time.Now().UnixNano(),
		"timeout":       40,
		"total_timeout": 120,
	}
	jb, err := json.Marshal(jsonPayload)
	if err != nil {
		return err
	}
	if err := writeXzyh(ch0, protocol.P2PCmdP2pSendFile, 0, jb); err != nil {
		return fmt.Errorf("write send_file json handshake: %w", err)
	}

	useLegacy := false
	select {
	case <-ctx.Done():
		return ctx.Err()
	case resp := <-xzyhReplyCh:
		if len(resp) >= 4 {
			code := binary.LittleEndian.Uint32(resp[:4])
			useLegacy = code != 0
		} else {
			useLegacy = true
		}
	case <-time.After(2 * time.Second):
		useLegacy = true
	}

	if useLegacy {
		if err := writeXzyh(ch0, protocol.P2PCmdP2pSendFile, 0, []byte(legacyID)); err != nil {
			return fmt.Errorf("write send_file legacy handshake: %w", err)
		}
	}

	// 2. Prepare metadata string
	// format: "type,name,size,md5,user_name,user_id,machine_id"
	h := md5.Sum(payload)
	md5Str := fmt.Sprintf("%x", h)
	meta := fmt.Sprintf("0,%s,%d,%s,%s,%s,%s", info.Name, info.Size, md5Str, info.UserName, info.UserID, fileUUID)
	metaData := append([]byte(meta), 0)

	// 3. Send BEGIN and wait for ACK (Python: aabb_request waits after each frame).
	begin := protocol.Aabb{FrameType: protocol.FileTransferBegin}
	bp, err := begin.PackWithCRC(metaData)
	if err != nil {
		return err
	}
	if _, _, err := ch.WriteContext(ctx, bp, true); err != nil {
		return fmt.Errorf("write aabb begin: %w", err)
	}
	if err := waitReply(); err != nil {
		return fmt.Errorf("aabb begin reply: %w", err)
	}

	// 4. Send DATA — 32KB blocks match the hardened Python blocksize.
	const (
		blockSize      = 1024 * 32
		maxDataRetries = 2
	)
	var pos int64
	for pos < info.Size {
		end := pos + int64(blockSize)
		if end > info.Size {
			end = info.Size
		}
		chunk := payload[pos:end]

		dataAabb := protocol.Aabb{
			FrameType: protocol.FileTransferData,
			Pos:       uint32(pos),
		}
		dp, err := dataAabb.PackWithCRC(chunk)
		if err != nil {
			return err
		}

		var lastErr error
		for attempt := 0; attempt <= maxDataRetries; attempt++ {
			if _, _, err := ch.WriteContext(ctx, dp, true); err != nil {
				return fmt.Errorf("write aabb data at %d: %w", pos, err)
			}
			lastErr = waitReply()
			if lastErr == nil {
				break
			}
			// Only retry on DRW-level timeouts, not application errors.
			if attempt < maxDataRetries && strings.Contains(lastErr.Error(), "timeout") {
				s.log.Warn("ppppservice: retrying file transfer chunk after transport timeout",
					"pos", pos, "attempt", attempt+2, "max", maxDataRetries+1)
				ch.ResetTx()
			}
		}
		if lastErr != nil {
			return fmt.Errorf("aabb data reply at %d: %w", pos, lastErr)
		}

		pos = end
		if progress != nil {
			progress(pos, info.Size)
		}
	}

	// 5. Optionally send END to trigger print start.
	if !info.StartPrint {
		return nil
	}
	endAabb := protocol.Aabb{FrameType: protocol.FileTransferEnd}
	ep, err := endAabb.PackWithCRC([]byte{})
	if err != nil {
		return err
	}
	if _, _, err := ch.WriteContext(ctx, ep, true); err != nil {
		return fmt.Errorf("write aabb end: %w", err)
	}

	if err := waitReply(); err != nil {
		return fmt.Errorf("aabb end reply: %w", err)
	}
	return nil
}

// WorkerStart establishes the PPPP client.
func (s *PPPPService) WorkerStart() error {
	ctx, cancel := context.WithTimeout(s.LoopContext(), 5*time.Second)
	defer cancel()

	cli, err := s.clientFactor(ctx)
	if err != nil {
		return err
	}
	s.clientMu.Lock()
	s.client = cli
	s.clientMu.Unlock()
	return nil
}

// connectTimeout is how long WorkerRun waits for the initial PPPP handshake
// (PunchPkt → StateConnected) before giving up and restarting.
// Python's pppp_open uses a 10-second deadline for the same wait.
const connectTimeout = 10 * time.Second

// WorkerRun blocks while PPPP is running and dispatches XZYH payloads.
func (s *PPPPService) WorkerRun(ctx context.Context) error {
	cli := s.currentClient()
	if cli == nil {
		return errors.New("ppppservice: no client")
	}

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- cli.Run(ctx)
	}()

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	connectDeadline := time.Now().Add(connectTimeout)
	ipPersisted := false // persist discovered IP once per connection
	connected := false

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-runErrCh:
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				s.log.Warn("pppp run loop failed", "err", err)
			}
			return ErrServiceRestartSignal
		case <-ticker.C:
			if cli.State() != ppppclient.StateConnected {
				if connected {
					s.log.Warn("ppppservice: connection lost, restarting")
					return ErrServiceRestartSignal
				}
				// Not yet connected — wait for PunchPkt handshake to complete.
				// Python waits up to 10 s for StateConnected; we do the same.
				now := time.Now()
				if now.After(connectDeadline) {
					s.log.Warn("ppppservice: connection timeout, restarting")
					return ErrServiceRestartSignal
				}
				continue
			}
			connected = true
			// Proactive staleness check: if the session is Connected but
			// Healthy() is false (no PingResp within ppppStaleThreshold),
			// the printer's Wi-Fi power saving has silently dropped the
			// session. Restart immediately rather than waiting for an upload
			// to time out. This keeps the service self-healing between
			// user-initiated operations.
			if !cli.Healthy() {
				s.log.Warn("ppppservice: session stale (no keepalive pong), restarting")
				return ErrServiceRestartSignal
			}
			// Persist discovered printer IP on first successful connection.
			// Guard: only persist valid unicast IPs — never broadcast/loopback/unspecified.
			if !ipPersisted {
				if ip := cli.RemoteIP(); util.IsValidPrinterIP(ip) {
					s.persistPrinterIP(ip.String())
					ipPersisted = true
				} else if ip != nil {
					s.log.Warn("ppppservice: RemoteIP is not a valid unicast address, skipping persist", "ip", ip)
				}
			}
			if err := s.drainAllXzyh(cli); err != nil {
				s.log.Warn("xzyh drain failed", "err", err)
				return ErrServiceRestartSignal
			}
		}
	}
}

// WorkerStop closes PPPP client.
func (s *PPPPService) WorkerStop() {
	s.clientMu.Lock()
	cli := s.client
	s.client = nil
	s.clientMu.Unlock()
	if cli != nil {
		if err := cli.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			s.log.Warn("pppp close failed", "err", err)
		}
	}
}

// persistPrinterIP saves the discovered LAN IP back to default.json and the DB.
// Called once per connection after the PPPP handshake completes.
// The printer SN is resolved dynamically from the config so this works even
// when the service was created before the user logged in.
func (s *PPPPService) persistPrinterIP(ipStr string) {
	if s.cfgMgr == nil {
		return
	}
	// Defense-in-depth: never persist an invalid IP (broadcast, loopback, etc.).
	if !util.IsValidPrinterIPString(ipStr) {
		s.log.Warn("ppppservice: refusing to persist invalid printer IP", "ip", ipStr)
		return
	}
	var printerSN string
	if err := s.cfgMgr.Modify(func(saved *model.Config) (*model.Config, error) {
		if saved == nil || s.printerIndex < 0 || s.printerIndex >= len(saved.Printers) {
			return saved, nil
		}
		saved.Printers[s.printerIndex].IPAddr = ipStr
		printerSN = saved.Printers[s.printerIndex].SN
		return saved, nil
	}); err != nil {
		s.log.Warn("ppppservice: failed to persist printer IP to config", "ip", ipStr, "err", err)
	}
	if s.database != nil && printerSN != "" {
		if err := s.database.SetPrinterIP(printerSN, ipStr); err != nil {
			s.log.Warn("ppppservice: failed to persist printer IP to db", "ip", ipStr, "err", err)
		}
	}
	s.log.Info("ppppservice: persisted printer IP", "ip", ipStr)
}

func (s *PPPPService) currentClient() ppppConn {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	return s.client
}

// IsConnected reports whether the current PPPP client handshake is connected.
func (s *PPPPService) IsConnected() bool {
	cli := s.currentClient()
	if cli == nil {
		return false
	}
	return cli.State() == ppppclient.StateConnected
}

// ProbePPPP performs a lightweight PPPP connectivity probe without touching the
// long-lived service registry instance used by the web UI and video pipeline.
func ProbePPPP(ctx context.Context, cfg *config.Manager, printerIndex int, database *db.DB) bool {
	return probePPPPWithFactory(ctx, defaultPPPPClientFactory(cfg, printerIndex, database))
}

func probePPPPWithFactory(ctx context.Context, factory ppppClientFactory) bool {
	if factory == nil {
		return false
	}

	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cli, err := factory(probeCtx)
	if err != nil {
		return false
	}
	defer func() { _ = cli.Close() }()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- cli.Run(probeCtx)
	}()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if cli.State() == ppppclient.StateConnected {
			return true
		}

		select {
		case <-probeCtx.Done():
			return cli.State() == ppppclient.StateConnected
		case <-ticker.C:
		case <-runErrCh:
			return cli.State() == ppppclient.StateConnected
		}
	}
}

func (s *PPPPService) drainAllXzyh(cli ppppConn) error {
	for ch := 0; ch < 8; ch++ {
		wire, err := cli.Channel(ch)
		if err != nil {
			return fmt.Errorf("get channel %d: %w", ch, err)
		}
		if err := s.drainXzyh(byte(ch), wire); err != nil {
			return fmt.Errorf("drain channel %d: %w", ch, err)
		}
	}
	return nil
}

func (s *PPPPService) drainXzyh(channel byte, ch *protocol.Channel) error {
	for {
		// Peek 12 bytes: enough to identify both AABB (Len at [8:12]) and
		// XZYH (Len at [6:10]) frames. Using 16 would miss short AABB replies
		// (e.g. 1-byte ACK = 12+1+2 = 15 bytes total).
		header := ch.Peek(12, 0)
		if len(header) == 0 {
			return nil
		}

		if header[0] == 0xAA && header[1] == 0xBB {
			if len(header) < 12 {
				return nil
			}
			sz := int(binary.LittleEndian.Uint32(header[8:12]))
			need := 12 + sz + 2
			frame := ch.Read(need, 0)
			if len(frame) == 0 {
				return nil
			}
			aabb, data, err := protocol.ParseAabbWithCRC(frame)
			if err != nil {
				s.log.Warn("aabb parse failed", "err", err)
				continue
			}
			s.dispatchAabb(channel, aabb, data)
			continue
		}

		if string(header[:4]) != "XZYH" {
			_ = ch.Read(1, 0)
			continue
		}
		sz := int(binary.LittleEndian.Uint32(header[6:10]))
		frame := ch.Read(16+sz, 0)
		if len(frame) == 0 {
			return nil
		}

		if channel == 1 && len(frame) >= 64 {
			// Channel 1 video frames have a 64-byte extended XZYH header.
			// Only attempt VideoFrame parse when the frame is large enough;
			// smaller XZYH frames on channel 1 (e.g. file transfer replies)
			// fall through to the generic XZYH path.
			vf, err := protocol.ParseVideoFrame(frame)
			if err != nil {
				s.log.Warn("video frame parse failed", "err", err)
				continue
			}
			s.dispatchVideo(vf)
		} else {
			x, err := protocol.ParseXzyh(frame)
			if err != nil {
				return err
			}
			s.dispatchXzyh(channel, x.Data)
		}
	}
}

func (s *PPPPService) dispatchXzyh(channel byte, payload []byte) {
	s.handlersMu.RLock()
	handlers := append([]func([]byte){}, s.handlers[channel]...)
	s.handlersMu.RUnlock()

	for _, h := range handlers {
		data := append([]byte(nil), payload...)
		h(data)
	}
}

func (s *PPPPService) dispatchVideo(vf protocol.VideoFrame) {
	s.handlersMu.RLock()
	handlers := append([]func(protocol.VideoFrame){}, s.videoHandlers...)
	s.handlersMu.RUnlock()

	for _, h := range handlers {
		h(vf)
	}
}

func (s *PPPPService) dispatchAabb(channel byte, aabb protocol.Aabb, data []byte) {
	s.handlersMu.RLock()
	handlers := append([]func(protocol.Aabb, []byte){}, s.aabbHandlers[channel]...)
	s.handlersMu.RUnlock()

	for _, h := range handlers {
		payloadCopy := append([]byte(nil), data...)
		h(aabb, payloadCopy)
	}
}
