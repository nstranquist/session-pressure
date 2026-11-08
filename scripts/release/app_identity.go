package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

type appIdentity struct {
	Exists  bool   `json:"exists"`
	Digest  string `json:"digest,omitempty"`
	Version string `json:"version,omitempty"`
	Build   string `json:"build,omitempty"`
}

func (a appIdentity) authority() string {
	if !a.Exists {
		return "none"
	}
	return a.Digest
}

func appBuildParts(value string) ([3]uint64, error) {
	var result [3]uint64
	parts := strings.Split(value, ".")
	if len(parts) > 3 || value == "" {
		return result, errors.New("invalid app build")
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil || part == "" || strings.HasPrefix(part, "+") {
			return result, errors.New("invalid app build")
		}
		result[i] = n
	}
	return result, nil
}

func readAppIdentity(path string) (appIdentity, error) {
	var result appIdentity
	if path == "" {
		return result, errors.New("app path required")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("app must be a regular directory")
	}
	f, err := os.Open(filepath.Join(path, "Contents", "Info.plist"))
	if err != nil {
		return result, err
	}
	defer f.Close()
	metadata, err := f.Stat()
	if err != nil {
		return result, err
	}
	if metadata.Size() > 1<<20 {
		return result, errors.New("app plist exceeds size bound")
	}
	var plist struct {
		XMLName xml.Name `xml:"plist"`
		Dict    struct {
			Entries []struct {
				XMLName xml.Name
				Text    string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err = xml.NewDecoder(io.LimitReader(f, (1<<20)+1)).Decode(&plist); err != nil {
		return result, fmt.Errorf("read app XML plist: %w", err)
	}
	values := map[string]string{}
	for i, entry := range plist.Dict.Entries {
		if entry.XMLName.Local != "key" {
			continue
		}
		switch entry.Text {
		case "CFBundleIdentifier", "CFBundleExecutable", "CFBundleShortVersionString", "CFBundleVersion":
			if _, duplicate := values[entry.Text]; duplicate || i+1 >= len(plist.Dict.Entries) || plist.Dict.Entries[i+1].XMLName.Local != "string" {
				return result, errors.New("invalid app identity plist")
			}
			values[entry.Text] = plist.Dict.Entries[i+1].Text
		}
	}
	if values["CFBundleIdentifier"] != "com.nstranquist.ndev-pressure" || values["CFBundleExecutable"] != "NDevPressure" {
		return result, errors.New("unexpected app identity")
	}
	result.Version, result.Build = values["CFBundleShortVersionString"], values["CFBundleVersion"]
	if !semver.IsValid("v" + result.Version) {
		return result, errors.New("app version must be SemVer")
	}
	if _, err := appBuildParts(result.Build); err != nil {
		return result, err
	}
	h := sha256.New()
	var count int
	var size int64
	err = filepath.WalkDir(path, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > 4096 {
			return errors.New("app exceeds file-count bound")
		}
		i, err := entry.Info()
		if err != nil {
			return err
		}
		if !i.IsDir() && !i.Mode().IsRegular() {
			return errors.New("app contains symlink or special file")
		}
		rel, err := filepath.Rel(path, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%d:%s:%s\x00", len(rel), rel, i.Mode().String())
		if i.IsDir() {
			return nil
		}
		if i.Size() < 0 || i.Size() > (512<<20)-size {
			return errors.New("app exceeds size bound")
		}
		size += i.Size()
		fmt.Fprintf(h, "%d:", i.Size())
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(h, io.LimitReader(file, i.Size()+1))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != i.Size() {
			return errors.New("app file changed while hashing")
		}
		return nil
	})
	if err != nil {
		return appIdentity{}, err
	}
	result.Exists, result.Digest = true, hex.EncodeToString(h.Sum(nil))
	return result, nil
}

func validateAppPromotion(current, candidate appIdentity, expected string, allowRollback bool) error {
	if expected == "" {
		return errors.New("app install requires --expected-current from app-status before the build")
	}
	if expected != current.authority() {
		return errors.New("app authority changed since expected-current was captured")
	}
	if !candidate.Exists {
		return errors.New("candidate app is missing")
	}
	if !current.Exists {
		return nil
	}
	comparison := semver.Compare("v"+candidate.Version, "v"+current.Version)
	if comparison == 0 {
		before, err := appBuildParts(current.Build)
		if err != nil {
			return err
		}
		after, err := appBuildParts(candidate.Build)
		if err != nil {
			return err
		}
		for i := range before {
			if after[i] < before[i] {
				comparison = -1
				break
			}
			if after[i] > before[i] {
				break
			}
		}
	}
	if comparison < 0 && !allowRollback {
		return errors.New("app version downgrade requires explicit --allow-rollback")
	}
	return nil
}
