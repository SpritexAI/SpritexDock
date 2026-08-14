package db

import "os"

func writeTestFile(path string) error {
	return os.WriteFile(path, []byte("not a directory"), 0600)
}
