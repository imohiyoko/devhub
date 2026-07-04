package updater

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// maxAsset caps the release download so a wrong/hostile URL can't stream an
// unbounded body onto disk. devhub archives are a few MB; 200 MB is generous.
const maxAsset = 200 << 20

// SelfUpdate downloads targetTag's release asset for this OS/arch, verifies it
// against the release checksums.txt (and optionally its cosign signature), then
// atomically swaps it in for the running executable. It does NOT restart — the
// caller re-execs after this returns so the response can be flushed first.
//
// Only meaningful for the installer edition (a single binary devhub owns the
// path to); the caller enforces that.
func SelfUpdate(ctx context.Context, targetTag string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("実行ファイルの解決に失敗: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	work, err := os.MkdirTemp("", "devhub-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	asset := assetName(targetTag)
	base := "https://github.com/" + repo() + "/releases/download/" + targetTag
	assetPath := filepath.Join(work, asset)
	sumsPath := filepath.Join(work, "checksums.txt")

	if err := download(ctx, base+"/"+asset, assetPath); err != nil {
		return fmt.Errorf("ダウンロードに失敗 (%s): %w", asset, err)
	}
	if err := download(ctx, base+"/checksums.txt", sumsPath); err != nil {
		return fmt.Errorf("checksums.txt の取得に失敗: %w", err)
	}

	// Optional cosign keyless verification, same contract as the install
	// scripts (off unless DEVHUB_VERIFY_SIGNATURE=1). Proves checksums.txt came
	// from this repo's release workflow — defends against a release that swaps
	// the binary AND its checksums.txt together, which SHA256 alone cannot catch.
	if os.Getenv("DEVHUB_VERIFY_SIGNATURE") == "1" {
		if err := verifyCosign(ctx, base, work, sumsPath); err != nil {
			return err
		}
	}

	if err := verifySHA256(assetPath, sumsPath, asset); err != nil {
		return err
	}

	// Extract the binary into the SAME directory as the current executable so
	// the final swap is a rename within one filesystem (atomic; no EXDEV).
	newFile, err := os.CreateTemp(filepath.Dir(exe), ".devhub-new-*")
	if err != nil {
		return fmt.Errorf("一時ファイル作成に失敗: %w", err)
	}
	newPath := newFile.Name()
	if err := extractBinary(assetPath, newFile); err != nil {
		newFile.Close()
		os.Remove(newPath)
		return fmt.Errorf("アーカイブ展開に失敗: %w", err)
	}
	if err := newFile.Close(); err != nil {
		os.Remove(newPath)
		return err
	}
	if err := os.Chmod(newPath, 0o755); err != nil {
		os.Remove(newPath)
		return err
	}

	if err := swapBinary(newPath, exe); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("バイナリ入替に失敗: %w", err)
	}
	return nil
}

// download streams url to destPath (bounded by maxAsset), honoring ctx.
func download(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "devhub-updater")
	resp, err := dlClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxAsset)); err != nil {
		return err
	}
	return nil
}

// verifySHA256 checks assetPath against the line for `asset` in checksums.txt.
func verifySHA256(assetPath, sumsPath, asset string) error {
	expected, err := checksumFor(sumsPath, asset)
	if err != nil {
		return err
	}
	f, err := os.Open(assetPath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("チェックサム検証に失敗しました (%s)", asset)
	}
	return nil
}

// checksumFor returns the hex SHA256 recorded for asset in a `<sha>  <name>`
// checksums.txt.
func checksumFor(sumsPath, asset string) (string, error) {
	f, err := os.Open(sumsPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s のチェックサムが見つかりません", asset)
}

// verifyCosign mirrors the install scripts' cosign verify-blob invocation. It
// requires the cosign CLI on PATH when DEVHUB_VERIFY_SIGNATURE=1.
func verifyCosign(ctx context.Context, base, work, sumsPath string) error {
	if _, err := exec.LookPath("cosign"); err != nil {
		return fmt.Errorf("cosign が見つかりません（DEVHUB_VERIFY_SIGNATURE=1 には cosign が必要です）")
	}
	sig := filepath.Join(work, "checksums.txt.sig")
	pem := filepath.Join(work, "checksums.txt.pem")
	if err := download(ctx, base+"/checksums.txt.sig", sig); err != nil {
		return fmt.Errorf("署名(.sig)の取得に失敗: %w", err)
	}
	if err := download(ctx, base+"/checksums.txt.pem", pem); err != nil {
		return fmt.Errorf("証明書(.pem)の取得に失敗: %w", err)
	}
	identity := "^https://github.com/" + repo() + "/\\.github/workflows/release\\.yml@refs/"
	cmd := exec.CommandContext(ctx, "cosign", "verify-blob", //execaudit:self-update-verify
		"--certificate", pem,
		"--signature", sig,
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		"--certificate-identity-regexp", identity,
		sumsPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("checksums.txt の署名検証に失敗しました: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// extractBinary finds the devhub executable inside a .tar.gz or .zip archive
// and copies it to out. The archive format follows GOOS (zip on windows).
func extractBinary(archivePath string, out io.Writer) error {
	if strings.HasSuffix(archivePath, ".zip") {
		return extractFromZip(archivePath, out)
	}
	return extractFromTarGz(archivePath, out)
}

func extractFromTarGz(archivePath string, out io.Writer) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	want := binName()
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if path.Base(hdr.Name) == want && hdr.Typeflag == tar.TypeReg {
			_, err := io.Copy(out, io.LimitReader(tr, maxAsset))
			return err
		}
	}
	return fmt.Errorf("%s がアーカイブ内に見つかりません", want)
}

func extractFromZip(archivePath string, out io.Writer) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	want := binName()
	for _, zf := range zr.File {
		if path.Base(zf.Name) == want {
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			_, err = io.Copy(out, io.LimitReader(rc, maxAsset))
			return err
		}
	}
	return fmt.Errorf("%s がアーカイブ内に見つかりません", want)
}
