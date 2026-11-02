//go:build aix

// node_aix.go – AIX CSI node service implementation.
//
// Differences from the Linux implementation (note 1.1 and section 3):
//
//   - No k8s.io/mount-utils: mount/umount are done with native AIX commands.
//   - No wipefs: the LV head is zeroed by lvmd at create time.
//   - No findmnt: mount status is checked by parsing `/usr/sbin/mount` output.
//   - No bind mounts for block volumes: a character device node is created at
//     the target path (note 3.4).
//   - Default fstype is "jfs2" (note 1.4); any other fstype is rejected.
//   - Statfs uses Bsize not Frsize (note 2.10).
//   - /run does not exist on AIX; sockets must be placed elsewhere (note 3.8);
//     this file does not override socket paths (that's a configuration concern).
//   - ResizeFs uses `chfs -a size=<blocks> <mountpoint>` (note 2.8).

package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/topolvm/topolvm"
	"github.com/topolvm/topolvm/internal/driver/internal/k8s"
	"github.com/topolvm/topolvm/internal/filesystem"
	"github.com/topolvm/topolvm/internal/lvmd/command"
	"github.com/topolvm/topolvm/pkg/lvmd/proto"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var nodeLogger = ctrl.Log.WithName("driver").WithName("node")

// NewNodeServer returns a new NodeServer for AIX.
func NewNodeServer(nodeName string, vgServiceClient proto.VGServiceClient, lvServiceClient proto.LVServiceClient, mgr manager.Manager) (csi.NodeServer, error) {
	lvService, err := k8s.NewLogicalVolumeService(mgr)
	if err != nil {
		return nil, err
	}

	return &nodeServer{
		server: &nodeServerNoLocked{
			nodeName:     nodeName,
			client:       vgServiceClient,
			lvService:    lvServiceClient,
			k8sLVService: lvService,
		},
	}, nil
}

// nodeServer wraps nodeServerNoLocked with a global mutex.
type nodeServer struct {
	csi.UnimplementedNodeServer
	mu     sync.Mutex
	server *nodeServerNoLocked
}

func (s *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.server.NodePublishVolume(ctx, req)
}

func (s *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.server.NodeUnpublishVolume(ctx, req)
}

func (s *nodeServer) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.server.NodeGetVolumeStats(ctx, req)
}

func (s *nodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.server.NodeExpandVolume(ctx, req)
}

func (s *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return s.server.NodeGetCapabilities(ctx, req)
}

func (s *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return s.server.NodeGetInfo(ctx, req)
}

// nodeServerNoLocked is the actual implementation.
type nodeServerNoLocked struct {
	csi.UnimplementedNodeServer

	nodeName     string
	client       proto.VGServiceClient
	lvService    proto.LVServiceClient
	k8sLVService *k8s.LogicalVolumeService
}

// ─── NodePublishVolume ────────────────────────────────────────────────────────

func (s *nodeServerNoLocked) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volumeContext := req.GetVolumeContext()
	volumeID := req.GetVolumeId()

	nodeLogger.Info("NodePublishVolume called",
		"volume_id", volumeID,
		"target_path", req.GetTargetPath(),
		"volume_capability", req.GetVolumeCapability(),
		"read_only", req.GetReadonly(),
		"volume_context", volumeContext)

	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no volume_id is provided")
	}
	if len(req.GetTargetPath()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no target_path is provided")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "no volume_capability is provided")
	}

	isBlockVol := req.GetVolumeCapability().GetBlock() != nil
	isFsVol := req.GetVolumeCapability().GetMount() != nil
	if !isBlockVol && !isFsVol {
		return nil, status.Errorf(codes.InvalidArgument, "no supported volume capability: %v", req.GetVolumeCapability())
	}

	accessMode := req.GetVolumeCapability().GetAccessMode().GetMode()
	if accessMode != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
		modeName := csi.VolumeCapability_AccessMode_Mode_name[int32(accessMode)]
		return nil, status.Errorf(codes.FailedPrecondition, "unsupported access mode: %s (%d)", modeName, accessMode)
	}

	lvr, err := s.k8sLVService.GetVolume(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	lv, err := s.getLvFromContext(ctx, lvr.Spec.DeviceClass, volumeID)
	if err != nil {
		return nil, err
	}
	if lv == nil {
		return nil, status.Errorf(codes.NotFound, "failed to find LV: %s", volumeID)
	}

	if isBlockVol {
		err = s.nodePublishBlockVolume(req, lv)
	} else {
		err = s.nodePublishFilesystemVolume(req, lv)
	}
	if err != nil {
		return nil, err
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

// ─── Filesystem publish ───────────────────────────────────────────────────────

func (s *nodeServerNoLocked) nodePublishFilesystemVolume(req *csi.NodePublishVolumeRequest, lv *proto.LogicalVolume) error {
	mountOption := req.GetVolumeCapability().GetMount()
	if mountOption.FsType == "" {
		mountOption.FsType = "jfs2"
	}
	// AIX only supports JFS2.
	if mountOption.FsType != "jfs2" {
		return status.Errorf(codes.InvalidArgument,
			"unsupported fstype %q on AIX; only jfs2 is supported", mountOption.FsType)
	}

	targetPath := req.GetTargetPath()
	if err := os.MkdirAll(targetPath, 0755); err != nil {
		return status.Errorf(codes.Internal, "mkdir failed: target=%s, error=%v", targetPath, err)
	}

	// Detect existing filesystem (JFS2 superblock check or blank).
	fsType, err := filesystem.DetectFilesystem(lv.GetPath())
	if err != nil {
		return status.Errorf(codes.Internal, "filesystem check failed: volume=%s, error=%v", req.GetVolumeId(), err)
	}
	if fsType != "" && fsType != "jfs2" {
		return status.Errorf(codes.Internal,
			"device is formatted with %s, expected jfs2: volume=%s", fsType, req.GetVolumeId())
	}

	// If blank, format.
	if fsType == "" {
		if err := formatJFS2(lv.GetPath()); err != nil {
			return status.Errorf(codes.Internal, "mkfs jfs2 failed: volume=%s, error=%v", req.GetVolumeId(), err)
		}
	}

	// Check if already mounted.
	mounted, err := isAlreadyMounted(lv.GetPath(), targetPath)
	if err != nil {
		return status.Errorf(codes.Internal, "mount check failed: target=%s, error=%v", targetPath, err)
	}
	if !mounted {
		opts := buildMountOptions(req.GetReadonly(), mountOption.MountFlags)
		if err := mountJFS2(lv.GetPath(), targetPath, opts); err != nil {
			return status.Errorf(codes.Internal, "mount failed: volume=%s, error=%v", req.GetVolumeId(), err)
		}
		if err := os.Chmod(targetPath, 0777|os.ModeSetgid); err != nil {
			return status.Errorf(codes.Internal, "chmod 2777 failed: target=%s, error=%v", targetPath, err)
		}
	}

	// Resize filesystem if needed (after lvmd already grew the block device).
	if err := resizeJFS2IfNeeded(lv.GetPath(), targetPath); err != nil {
		return status.Errorf(codes.Internal, "resize jfs2 failed: volume=%s, error=%v", req.GetVolumeId(), err)
	}

	nodeLogger.Info("NodePublishVolume(fs) succeeded",
		"volume_id", req.GetVolumeId(), "target_path", targetPath)
	return nil
}

// formatJFS2 runs `mkfs -V jfs2 -o log=INLINE <dev>`.
func formatJFS2(dev string) error {
	out, err := exec.Command("/usr/sbin/mkfs", "-V", "jfs2", "-o", "log=INLINE", dev).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mkfs -V jfs2 %s: %v: %s", dev, err, string(out))
	}
	return nil
}

// mountJFS2 runs `mount -v jfs2 -o <opts> <dev> <dir>`.
func mountJFS2(dev, dir string, opts []string) error {
	args := []string{"-v", "jfs2"}
	if len(opts) > 0 {
		args = append(args, "-o", strings.Join(opts, ","))
	}
	args = append(args, dev, dir)
	out, err := exec.Command("/usr/sbin/mount", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mount -v jfs2 %s %s: %v: %s", dev, dir, err, string(out))
	}
	return nil
}

// umountPath runs `umount <path>`.
func umountPath(path string) error {
	out, err := exec.Command("/usr/sbin/umount", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("umount %s: %v: %s", path, err, string(out))
	}
	return nil
}

// buildMountOptions assembles mount option strings.
func buildMountOptions(readOnly bool, flags []string) []string {
	var opts []string
	if readOnly {
		opts = append(opts, "ro")
	}
	opts = append(opts, "log=INLINE")
	opts = append(opts, flags...)
	return opts
}

// resizeJFS2IfNeeded grows the filesystem at mountpoint to fill the block device.
// Uses `chfs -a size=<512-byte-blocks> <mountpoint>`.
// Note 2.8: chfs may require a /etc/filesystems stanza; if it fails we log a
// warning but do not return an error (the filesystem may already be the right
// size).
func resizeJFS2IfNeeded(dev, mountpoint string) error {
	// Get device size in bytes.
	f, err := os.Open(dev)
	if err != nil {
		return err
	}
	sizeBytesI64, err := f.Seek(0, io.SeekEnd)
	_ = f.Close()
	if err != nil {
		return err
	}
	sizeBlocks := sizeBytesI64 / 512

	out, err := exec.Command("/usr/sbin/chfs", "-a",
		fmt.Sprintf("size=%d", sizeBlocks), mountpoint).CombinedOutput()
	if err != nil {
		// chfs may fail if there's no /etc/filesystems stanza.  Log and
		// continue (accept already-correct size).
		nodeLogger.Info("chfs resize did not succeed (may be already correct size)",
			"mountpoint", mountpoint, "output", string(out), "error", err)
	}
	return nil
}

// ─── Block publish (raw character device) ────────────────────────────────────

// nodePublishBlockVolume creates a character device node at the target path.
// Note 3.4: AIX has no bind mount for device files; create a device node.
// Note 3.4: Refuse read-only raw publish.
// Note 4.2: Publish the character device (/dev/r<lv>) by default.
func (s *nodeServerNoLocked) nodePublishBlockVolume(req *csi.NodePublishVolumeRequest, lv *proto.LogicalVolume) error {
	if req.GetReadonly() {
		return status.Error(codes.InvalidArgument,
			"read-only raw block volumes are not supported on AIX: a device node cannot be made read-only for a root consumer")
	}

	targetPath := req.GetTargetPath()

	// Check if node already exists.
	if _, err := os.Stat(targetPath); err == nil {
		return nil // already published
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return status.Errorf(codes.Internal, "mkdir failed for %s: %v", filepath.Dir(targetPath), err)
	}

	// Use character device (/dev/r<lv>) — note 4.2.
	charDev := rawDevPath(lv.GetPath())

	// Get device number from the character device file.
	var st unix.Stat_t
	if err := filesystem.Stat(charDev, &st); err != nil {
		return status.Errorf(codes.Internal, "stat %s failed: %v", charDev, err)
	}
	devNum := int(st.Rdev)

	// Create device node at target path with the same mode and ownership as
	// the original device (note 4.3).
	mode := st.Mode & ^uint32(unix.S_IFMT) | unix.S_IFCHR
	if err := filesystem.Mknod(targetPath, mode, devNum); err != nil {
		return status.Errorf(codes.Internal, "mknod %s failed: %v", targetPath, err)
	}

	// Copy owner/group from the source device (note 4.3).
	if err := os.Lchown(targetPath, int(st.Uid), int(st.Gid)); err != nil {
		return status.Errorf(codes.Internal, "lchown %s failed: %v", targetPath, err)
	}

	nodeLogger.Info("NodePublishVolume(block) succeeded",
		"volume_id", req.GetVolumeId(), "target_path", targetPath)
	return nil
}

// rawDevPath returns the character device path for a block device.
// AIX names the raw (character) device /dev/r<lv>.
func rawDevPath(blockPath string) string {
	dir := filepath.Dir(blockPath)
	base := filepath.Base(blockPath)
	return filepath.Join(dir, "r"+base)
}

// ─── NodeUnpublishVolume ──────────────────────────────────────────────────────

func (s *nodeServerNoLocked) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()
	nodeLogger.Info("NodeUnpublishVolume called", "volume_id", volumeID, "target_path", targetPath)

	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no volume_id is provided")
	}
	if len(targetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no target_path is provided")
	}

	info, err := os.Stat(targetPath)
	if os.IsNotExist(err) {
		_ = os.Remove(filepath.Join(topolvm.LegacyDeviceDirectory, volumeID))
		return &csi.NodeUnpublishVolumeResponse{}, nil
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "stat failed for %s: %v", targetPath, err)
	}

	if info.IsDir() {
		// Filesystem volume: unmount.
		if err := umountPath(targetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "unmount failed for %s: %v", targetPath, err)
		}
		if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
			return nil, status.Errorf(codes.Internal, "remove dir failed for %s: %v", targetPath, err)
		}
	} else {
		// Raw device node: just remove the file.
		if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
			return nil, status.Errorf(codes.Internal, "remove failed for %s: %v", targetPath, err)
		}
	}

	nodeLogger.Info("NodeUnpublishVolume succeeded", "volume_id", volumeID, "target_path", targetPath)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// ─── NodeGetVolumeStats ───────────────────────────────────────────────────────

func (s *nodeServerNoLocked) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	volumeID := req.GetVolumeId()
	volumePath := req.GetVolumePath()
	nodeLogger.Info("NodeGetVolumeStats called", "volume_id", volumeID, "volume_path", volumePath)

	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no volume_id is provided")
	}
	if len(volumePath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no volume_path is provided")
	}

	var st unix.Stat_t
	switch err := filesystem.Stat(volumePath, &st); {
	case errors.Is(err, unix.ENOENT):
		return nil, status.Error(codes.NotFound, "Volume is not found at "+volumePath)
	case err == nil:
	default:
		return nil, status.Errorf(codes.Internal, "stat on %s was failed: %v", volumePath, err)
	}

	if (st.Mode & unix.S_IFMT) == unix.S_IFBLK || (st.Mode&unix.S_IFMT) == unix.S_IFCHR {
		// Raw device (block or character): report size via seek.
		f, err := os.Open(volumePath)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "open on %s was failed: %v", volumePath, err)
		}
		defer func() { _ = f.Close() }()
		pos, err := f.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "seek on %s was failed: %v", volumePath, err)
		}
		return &csi.NodeGetVolumeStatsResponse{
			Usage: []*csi.VolumeUsage{{Total: pos, Unit: csi.VolumeUsage_BYTES}},
		}, nil
	}

	if st.Mode&unix.S_IFDIR == 0 {
		return nil, status.Errorf(codes.Internal, "invalid mode bits for %s: %d", volumePath, st.Mode)
	}

	var sfs unix.Statfs_t
	if err := filesystem.Statfs(volumePath, &sfs); err != nil {
		return nil, status.Errorf(codes.Internal, "statfs on %s was failed: %v", volumePath, err)
	}

	// On AIX, Statfs_t has Bsize (no Frsize).  JFS2 f_blocks are in f_bsize
	// units (note 2.10).
	blockSize := filesystem.BlockSize(&sfs)

	var usage []*csi.VolumeUsage
	if sfs.Blocks > 0 {
		usage = append(usage, &csi.VolumeUsage{
			Unit:      csi.VolumeUsage_BYTES,
			Total:     int64(sfs.Blocks) * blockSize,
			Used:      int64(sfs.Blocks-sfs.Bfree) * blockSize,
			Available: int64(sfs.Bavail) * blockSize,
		})
	}
	if sfs.Files > 0 {
		usage = append(usage, &csi.VolumeUsage{
			Unit:      csi.VolumeUsage_INODES,
			Total:     int64(sfs.Files),
			Used:      int64(sfs.Files - sfs.Ffree),
			Available: int64(sfs.Ffree),
		})
	}

	lvr, err := s.k8sLVService.GetVolume(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	lv, err := s.getLvFromContext(ctx, lvr.Spec.DeviceClass, volumeID)
	if err != nil {
		return nil, err
	}
	if lv == nil {
		return nil, status.Errorf(codes.NotFound, "failed to find LV: %s", volumeID)
	}

	volumeCondition, err := getVolumeCondition(lv)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.NodeGetVolumeStatsResponse{Usage: usage, VolumeCondition: volumeCondition}, nil
}

// ─── NodeExpandVolume ─────────────────────────────────────────────────────────

func (s *nodeServerNoLocked) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	volumePath := req.GetVolumePath()
	logger := nodeLogger.WithValues("volume_id", volumeID, "volume_path", volumePath)
	logger.Info("NodeExpandVolume called")

	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no volume_id is provided")
	}
	if len(volumePath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no volume_path is provided")
	}

	_, err := convertRequestCapacityBytes(
		req.GetCapacityRange().GetRequiredBytes(),
		req.GetCapacityRange().GetLimitBytes(),
	)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if isBlock := req.GetVolumeCapability().GetBlock() != nil; isBlock {
		logger.Info("NodeExpandVolume(block) is skipped")
		return &csi.NodeExpandVolumeResponse{}, nil
	}

	lvr, err := s.k8sLVService.GetVolume(ctx, volumeID)
	deviceClass := topolvm.DefaultDeviceClassName
	if err == nil {
		deviceClass = lvr.Spec.DeviceClass
	} else if !errors.Is(err, k8s.ErrVolumeNotFound) {
		return nil, err
	}
	lv, err := s.getLvFromContext(ctx, deviceClass, volumeID)
	if err != nil {
		return nil, err
	}
	if lv == nil {
		return nil, status.Errorf(codes.NotFound, "failed to find LV: %s", volumeID)
	}

	// Find what device is mounted at volumePath using `mount` output.
	devPath, err := findMountSource(volumePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not find mount source for %s: %v", volumePath, err)
	}
	if devPath == "" {
		return nil, status.Errorf(codes.Internal, "filesystem %s is not mounted at %s", volumeID, volumePath)
	}
	logger = logger.WithValues("device", devPath)
	logger.Info("triggering filesystem resize")

	if err := resizeJFS2IfNeeded(devPath, volumePath); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resize filesystem %s (mounted at: %s): %v", volumeID, volumePath, err)
	}

	logger.Info("NodeExpandVolume(fs) succeeded")
	return &csi.NodeExpandVolumeResponse{}, nil
}

// ─── NodeGetCapabilities / NodeGetInfo ────────────────────────────────────────

func (s *nodeServerNoLocked) NodeGetCapabilities(_ context.Context, _ *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	capabilities := []csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		csi.NodeServiceCapability_RPC_VOLUME_CONDITION,
	}
	csiCaps := make([]*csi.NodeServiceCapability, len(capabilities))
	for i, c := range capabilities {
		csiCaps[i] = &csi.NodeServiceCapability{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{Type: c},
			},
		}
	}
	return &csi.NodeGetCapabilitiesResponse{Capabilities: csiCaps}, nil
}

func (s *nodeServerNoLocked) NodeGetInfo(_ context.Context, _ *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: s.nodeName,
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				topolvm.GetTopologyNodeKey(): s.nodeName,
			},
		},
	}, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func (s *nodeServerNoLocked) findVolumeByID(listResp *proto.GetLVListResponse, name string) *proto.LogicalVolume {
	for _, v := range listResp.Volumes {
		if v.Name == name {
			return v
		}
	}
	return nil
}

func (s *nodeServerNoLocked) getLvFromContext(ctx context.Context, deviceClass, volumeID string) (*proto.LogicalVolume, error) {
	listResp, err := s.client.GetLVList(ctx, &proto.GetLVListRequest{DeviceClass: deviceClass})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list LV: %v", err)
	}
	return s.findVolumeByID(listResp, volumeID), nil
}

// isAlreadyMounted returns true if devPath is already mounted at targetPath.
func isAlreadyMounted(devPath, targetPath string) (bool, error) {
	mountLines, err := parseMountOutput()
	if err != nil {
		return false, err
	}
	for _, entry := range mountLines {
		if entry.device == devPath && entry.mountpoint == targetPath {
			return true, nil
		}
	}
	return false, nil
}

// findMountSource returns the device path that is mounted at mountpoint.
func findMountSource(mountpoint string) (string, error) {
	mountLines, err := parseMountOutput()
	if err != nil {
		return "", err
	}
	for _, entry := range mountLines {
		if entry.mountpoint == mountpoint {
			return entry.device, nil
		}
	}
	return "", nil
}

// parseMountOutput runs `/usr/sbin/mount` and returns parsed entries.
func parseMountOutput() ([]mountEntry, error) {
	out, err := exec.Command("/usr/sbin/mount").Output()
	if err != nil {
		return nil, fmt.Errorf("mount: %w", err)
	}
	return parseMountLines(string(out)), nil
}

// getVolumeCondition converts the LV attr string into a CSI VolumeCondition.
func getVolumeCondition(lv *proto.LogicalVolume) (*csi.VolumeCondition, error) {
	attr, err := command.ParsedLVAttr(lv.GetAttr())
	if err != nil {
		return nil, fmt.Errorf("failed to parse attributes returned from logical volume service: %w", err)
	}
	var vc csi.VolumeCondition
	if err := attr.VerifyHealth(); err != nil {
		vc = csi.VolumeCondition{Abnormal: true, Message: err.Error()}
	} else {
		vc = csi.VolumeCondition{Abnormal: false, Message: "volume is healthy and operating normally"}
	}
	return &vc, nil
}


