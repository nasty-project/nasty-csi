#!/bin/bash
# Cleanup script for NASty
# Removes test subvolumes, snapshots, and shares from a specified filesystem.
#
# Usage:
#   ./scripts/cleanup-all-nasty-resources.sh              # Safe mode: only test-* artifacts
#   ./scripts/cleanup-all-nasty-resources.sh --all         # ALL subvolumes in filesystem (DANGEROUS!)
#   ./scripts/cleanup-all-nasty-resources.sh --dry-run     # Show what would be deleted
#
# Required environment variables:
#   NASTY_HOST      NASty hostname/IP
#   NASTY_API_KEY   API token for authentication
#   NASTY_FILESYSTEM      Filesystem name (default: first)

set -e

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

MODE="safe"
DRY_RUN=false

for arg in "$@"; do
    case $arg in
        --all)    MODE="all" ;;
        --dry-run) DRY_RUN=true ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --all       Remove ALL subvolumes and shares in the filesystem (DANGEROUS!)"
            echo "  --dry-run   Show what would be deleted without actually deleting"
            echo "  --help      Show this help message"
            echo ""
            echo "Default mode (safe): Only removes test artifacts (test-*, pvc-*)"
            echo ""
            echo "Required environment variables:"
            echo "  NASTY_HOST      NASty hostname/IP"
            echo "  NASTY_API_KEY   API token for authentication"
            echo "  NASTY_FILESYSTEM      Filesystem name (default: first)"
            exit 0
            ;;
        *) echo -e "${RED}Unknown option: $arg${NC}"; exit 1 ;;
    esac
done

: "${NASTY_HOST:?NASTY_HOST not set}"
: "${NASTY_API_KEY:?NASTY_API_KEY not set}"
: "${NASTY_FILESYSTEM:=first}"

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}NASty Cleanup Script${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""
echo -e "${YELLOW}Host:${NC} ${NASTY_HOST}"
echo -e "${YELLOW}Filesystem:${NC} ${NASTY_FILESYSTEM}"
echo -e "${YELLOW}Mode:${NC} ${MODE}"
$DRY_RUN && echo -e "${YELLOW}Dry Run:${NC} Enabled"
echo ""

if [ "$MODE" = "all" ]; then
    echo -e "${RED}WARNING: You are about to delete ALL subvolumes and shares in filesystem '${NASTY_FILESYSTEM}'${NC}"
    read -p "Type 'DELETE ALL' to confirm: " CONFIRM
    if [ "$CONFIRM" != "DELETE ALL" ]; then
        echo -e "${YELLOW}Cancelled${NC}"
        exit 0
    fi
fi

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLEANUP_DIR=$(mktemp -d)

cat > "$CLEANUP_DIR/main.go" <<'EOFGO'
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	nastyapi "github.com/nasty-project/nasty-go"
)

func main() {
	host := os.Getenv("NASTY_HOST")
	apiKey := os.Getenv("NASTY_API_KEY")
	filesystem := os.Getenv("NASTY_FILESYSTEM")
	mode := os.Getenv("CLEANUP_MODE")
	dryRun := os.Getenv("DRY_RUN") == "true"

	url := fmt.Sprintf("wss://%s/ws", host)
	fmt.Printf("Connecting to NASty at %s...\n", url)

	client, err := nastyapi.NewClient(url, apiKey, true, nil)
	if err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	ctx := context.Background()

	// --- Delete shares first (NFS, SMB, iSCSI, NVMe-oF) ---

	// NFS
	fmt.Println("\n=== NFS Shares ===")
	nfsShares, err := client.ListNFSShares(ctx)
	if err != nil {
		fmt.Printf("Warning: %v\n", err)
	}
	nfsDeleted := 0
	for _, s := range nfsShares {
		if !shouldClean(s.Path, filesystem, mode) {
			continue
		}
		fmt.Printf("  %s (ID: %s)\n", s.Path, s.ID)
		if !dryRun {
			if err := client.DeleteNFSShare(ctx, s.ID); err != nil {
				fmt.Printf("    ⚠ %v\n", err)
			} else {
				fmt.Printf("    ✓ Deleted\n")
				nfsDeleted++
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// SMB
	fmt.Println("\n=== SMB Shares ===")
	smbShares, err := client.ListSMBShares(ctx)
	if err != nil {
		fmt.Printf("Warning: %v\n", err)
	}
	smbDeleted := 0
	for _, s := range smbShares {
		if !shouldClean(s.Path, filesystem, mode) {
			continue
		}
		fmt.Printf("  %s (ID: %s)\n", s.Path, s.ID)
		if !dryRun {
			if err := client.DeleteSMBShare(ctx, s.ID); err != nil {
				fmt.Printf("    ⚠ %v\n", err)
			} else {
				fmt.Printf("    ✓ Deleted\n")
				smbDeleted++
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// iSCSI
	fmt.Println("\n=== iSCSI Targets ===")
	iscsiTargets, err := client.ListISCSITargets(ctx)
	if err != nil {
		fmt.Printf("Warning: %v\n", err)
	}
	iscsiDeleted := 0
	for _, t := range iscsiTargets {
		if !shouldCleanName(t.IQN, mode) {
			continue
		}
		fmt.Printf("  %s (ID: %s)\n", t.IQN, t.ID)
		if !dryRun {
			if err := client.DeleteISCSITarget(ctx, t.ID); err != nil {
				fmt.Printf("    ⚠ %v\n", err)
			} else {
				fmt.Printf("    ✓ Deleted\n")
				iscsiDeleted++
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// NVMe-oF
	fmt.Println("\n=== NVMe-oF Subsystems ===")
	nvmeofSubs, err := client.ListNVMeOFSubsystems(ctx)
	if err != nil {
		fmt.Printf("Warning: %v\n", err)
	}
	nvmeofDeleted := 0
	for _, s := range nvmeofSubs {
		if !shouldCleanName(s.NQN, mode) {
			continue
		}
		fmt.Printf("  %s (ID: %s)\n", s.NQN, s.ID)
		if !dryRun {
			if err := client.DeleteNVMeOFSubsystem(ctx, s.ID); err != nil {
				fmt.Printf("    ⚠ %v\n", err)
			} else {
				fmt.Printf("    ✓ Deleted\n")
				nvmeofDeleted++
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// --- Delete snapshots ---
	fmt.Println("\n=== Snapshots ===")
	snapshots, err := client.ListSnapshots(ctx, filesystem)
	if err != nil {
		fmt.Printf("Warning: %v\n", err)
	}
	snapDeleted := 0
	for _, snap := range snapshots {
		if !shouldCleanName(snap.Subvolume, mode) && !shouldCleanName(snap.Name, mode) {
			continue
		}
		fmt.Printf("  %s/%s@%s\n", snap.Filesystem, snap.Subvolume, snap.Name)
		if !dryRun {
			if err := client.DeleteSnapshot(ctx, snap.Filesystem, snap.Subvolume, snap.Name); err != nil {
				fmt.Printf("    ⚠ %v\n", err)
			} else {
				fmt.Printf("    ✓ Deleted\n")
				snapDeleted++
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// --- Delete subvolumes ---
	fmt.Println("\n=== Subvolumes ===")
	subvols, err := client.ListAllSubvolumes(ctx, filesystem)
	if err != nil {
		fmt.Printf("Failed to list subvolumes: %v\n", err)
		os.Exit(1)
	}
	subvolDeleted := 0
	for _, sv := range subvols {
		if !shouldCleanName(sv.Name, mode) {
			continue
		}
		fmt.Printf("  %s/%s (%s)\n", sv.Filesystem, sv.Name, sv.SubvolumeType)
		if !dryRun {
			if err := client.DeleteSubvolume(ctx, sv.Filesystem, sv.Name); err != nil {
				fmt.Printf("    ⚠ %v\n", err)
			} else {
				fmt.Printf("    ✓ Deleted\n")
				subvolDeleted++
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Summary
	fmt.Println("\n=== Summary ===")
	if dryRun {
		fmt.Println("(dry run — nothing was deleted)")
	}
	fmt.Printf("NFS shares:        %d deleted\n", nfsDeleted)
	fmt.Printf("SMB shares:        %d deleted\n", smbDeleted)
	fmt.Printf("iSCSI targets:     %d deleted\n", iscsiDeleted)
	fmt.Printf("NVMe-oF subsystems:%d deleted\n", nvmeofDeleted)
	fmt.Printf("Snapshots:         %d deleted\n", snapDeleted)
	fmt.Printf("Subvolumes:        %d deleted\n", subvolDeleted)
	fmt.Println("\n✓ Cleanup complete!")
}

func shouldClean(path, filesystem, mode string) bool {
	if mode == "all" {
		return strings.Contains(path, "/"+filesystem+"/")
	}
	return isTestArtifact(path)
}

func shouldCleanName(name, mode string) bool {
	if mode == "all" {
		return true
	}
	return isTestArtifact(name)
}

func isTestArtifact(s string) bool {
	return strings.Contains(s, "test-") ||
		strings.Contains(s, "pvc-") ||
		strings.Contains(s, "csi-detached") ||
		strings.Contains(s, "clone-tmp-")
}
EOFGO

echo -e "${YELLOW}Building cleanup tool...${NC}"

cd "$CLEANUP_DIR"
go mod init cleanup
go mod edit -require github.com/nasty-project/nasty-go@v0.0.0
go mod edit -replace "github.com/nasty-project/nasty-go=$SCRIPT_DIR/../nasty-go"
go mod tidy

echo -e "${YELLOW}Running cleanup...${NC}"
echo ""

CLEANUP_MODE="$MODE" DRY_RUN="$DRY_RUN" go run main.go

cd "$SCRIPT_DIR"
rm -rf "$CLEANUP_DIR"

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Done!${NC}"
echo -e "${GREEN}========================================${NC}"
