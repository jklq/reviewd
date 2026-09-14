package github

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
)

func (c *Client) MergeBase(ctx context.Context, repo, base, head string) (string, error) {
	var v struct {
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	err := c.Do(ctx, "GET", fmt.Sprintf("/repos/%s/compare/%s...%s", repo, base, head), nil, &v)
	if err != nil {
		return "", err
	}
	if !ValidSHA(v.MergeBase.SHA) {
		return "", errors.New("invalid merge base")
	}
	return v.MergeBase.SHA, nil
}
func (c *Client) Snapshot(ctx context.Context, repo, sha, dest string) error {
	if !ValidRepo(repo) || !ValidSHA(sha) {
		return errors.New("invalid snapshot ref")
	}
	t, err := c.Token(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.App.BaseURL+"/repos/"+repo+"/tarball/"+sha, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t)
	req.Header.Set("User-Agent", "reviewd")
	resp, err := c.App.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("snapshot: HTTP %d", resp.StatusCode)
	}
	return Extract(resp.Body, dest)
}

// Extract rejects traversal, special files and escaping links. No archive mode
// bits beyond executable are retained. Git submodules/LFS are not fetched.
func Extract(src io.Reader, dest string) error {
	if err := os.MkdirAll(dest, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	gz, err := gzip.NewReader(io.LimitReader(src, 256<<20))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	count := 0
	prefix := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// Git archive prepends a PAX global header ("pax_global_header") that
		// Go's tar reader surfaces as an entry; it is not part of the tree.
		if h.Typeflag == tar.TypeXGlobalHeader || h.Typeflag == tar.TypeXHeader {
			continue
		}
		count++
		if count > 100000 {
			return errors.New("snapshot exceeds 100000 entries")
		}
		parts := strings.SplitN(h.Name, "/", 2)
		if prefix == "" {
			prefix = parts[0]
		}
		if parts[0] != prefix || prefix == "" || prefix == ".." {
			return errors.New("invalid archive root")
		}
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		p := strings.TrimSuffix(parts[1], "/")
		if path.Clean(p) != p || strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "\\") {
			return errors.New("unsafe archive path")
		}
		if err = root.MkdirAll(path.Dir(p), 0755); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err = root.MkdirAll(p, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			total += h.Size
			if h.Size < 0 || total > 1<<30 {
				return errors.New("snapshot exceeds 1 GiB")
			}
			mode := os.FileMode(0644)
			if h.Mode&0111 != 0 {
				mode = 0755
			}
			f, err := root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, err = io.CopyN(f, tr, h.Size)
			ce := f.Close()
			if err != nil {
				return err
			}
			if ce != nil {
				return ce
			}
		case tar.TypeSymlink:
			target := path.Clean(path.Join(path.Dir(p), h.Linkname))
			if strings.HasPrefix(h.Linkname, "/") || target == ".." || strings.HasPrefix(target, "../") || strings.Contains(h.Linkname, "\\") {
				return errors.New("snapshot contains an escaping symlink")
			}
			if err = root.Symlink(h.Linkname, p); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archive entry type %d", h.Typeflag)
		}
	}
}
