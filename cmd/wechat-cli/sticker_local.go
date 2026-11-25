package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// WeChat 4.x keeps every sticker it has shown in its own cache,
// <account>/cache/<YYYY-MM>/Emoticon/<xx>/<md5> and
// <account>/business/emoticon/Persist/<xx>/<md5>, encrypted with
// AES-128-CBC where key = iv = md5("<uin><wxid>EMOTICON"). The uin is the
// number in kvcomm's key_<uin>_*.statistic file names; wxid is the account
// directory name without its 4-hex suffix. Some entries hold wxgf (WeChat's
// HEVC container) instead of the original image, so a decryption counts only
// when the bytes hash to the sticker md5 itself.

const stickerLocalMaxBytes = 32 << 20

func (s *server) stickerLocalKeys() [][]byte {
	s.stickerKeysOnce.Do(func() {
		if s.cfg == nil || s.cfg.DBRoot == "" {
			return
		}
		root := filepath.Clean(s.cfg.DBRoot)
		leaf := filepath.Base(root)
		wxids := []string{leaf}
		if w := stickerWxid(leaf); w != leaf {
			wxids = append([]string{w}, wxids...)
		}
		for _, uin := range kvcommUins(filepath.Dir(filepath.Dir(root))) {
			for _, wxid := range wxids {
				sum := md5.Sum([]byte(uin + wxid + "EMOTICON"))
				s.stickerKeys = append(s.stickerKeys, sum[:])
			}
		}
	})
	return s.stickerKeys
}

// stickerWxid drops the 4-hex directory suffix: wxid_abc_1f2e -> wxid_abc.
func stickerWxid(dir string) string {
	if idx := strings.LastIndex(dir, "_"); idx > 0 {
		if suffix := dir[idx+1:]; len(suffix) == 4 {
			if _, err := hex.DecodeString(suffix); err == nil {
				return dir[:idx]
			}
		}
	}
	return dir
}

// kvcommUins lists the uins named by kvcomm files under WeChat's Documents
// directory (the parent of xwechat_files).
func kvcommUins(documents string) []string {
	var dirs []string
	for _, pattern := range []string{"*/net/kvcomm", "app_data/*/kvcomm", "app_data/*/*/kvcomm", "app_data/*/*/*/kvcomm"} {
		matches, _ := filepath.Glob(filepath.Join(documents, pattern))
		dirs = append(dirs, matches...)
	}
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			rest, ok := strings.CutPrefix(entry.Name(), "key_")
			if !ok {
				continue
			}
			uin, _, ok := strings.Cut(rest, "_")
			if _, err := strconv.ParseUint(uin, 10, 32); !ok || err != nil || seen[uin] {
				continue
			}
			seen[uin] = true
			out = append(out, uin)
		}
	}
	return out
}

// localStickerImage returns the original sticker bytes from WeChat's cache,
// or nil when it is absent, stored as wxgf, or cannot be decrypted.
func (s *server) localStickerImage(sum string) []byte {
	if s.cfg == nil || s.cfg.DBRoot == "" || !stickerMD5RE.MatchString(sum) {
		return nil
	}
	keys := s.stickerLocalKeys()
	if len(keys) == 0 {
		return nil
	}
	root := filepath.Clean(s.cfg.DBRoot)
	candidates, _ := filepath.Glob(filepath.Join(root, "cache", "*", "Emoticon", sum[:2], sum))
	candidates = append(candidates, filepath.Join(root, "business", "emoticon", "Persist", sum[:2], sum))
	for _, path := range candidates {
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > stickerLocalMaxBytes || st.Size()%aes.BlockSize != 0 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, key := range keys {
			if plain := decryptStickerCache(data, key); plain != nil {
				if got := md5.Sum(plain); hex.EncodeToString(got[:]) == sum && stickerImageExt(plain) != "" {
					return plain
				}
			}
		}
	}
	return nil
}

func decryptStickerCache(data, key []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil
	}
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, key).CryptBlocks(plain, data)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize {
		return nil
	}
	return plain[:len(plain)-pad]
}
