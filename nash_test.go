package nash

import (
	"bytes"
	"os"
	"testing"
)

var (
	testDir, gopath, nashdPath string
)

func init() {
	gopath = os.Getenv("GOPATH")

	if gopath == "" {
		panic("Please, run tests from inside GOPATH")
	}

	testDir = gopath + "/src/github.com/tiago4orion/nash/" + "testfiles"
	nashdPath = gopath + "/src/github.com/tiago4orion/nash/cmd/nash/nash"

	if _, err := os.Stat(nashdPath); err != nil {
		panic("Please, run make build before running tests")
	}
}

func TestExecuteFile(t *testing.T) {
	testfile := testDir + "/ex1.sh"

	var out bytes.Buffer

	sh := NewShell(false)
	sh.SetNashdPath(nashdPath)
	sh.SetStdout(&out)

	err := sh.Execute(testfile)

	if err != nil {
		t.Error(err)
		return
	}

	if string(out.Bytes()) != "hello world\n" {
		t.Errorf("Wrong command output: '%s'", string(out.Bytes()))
		return
	}
}

func TestExecuteRfork(t *testing.T) {
	var out bytes.Buffer

	sh := NewShell(false)
	sh.SetNashdPath(nashdPath)
	sh.SetStdout(&out)

	err := sh.ExecuteString("rfork test", `
        rfork u {
            id -u
        }
        `)

	if err != nil {
		t.Error(err)
	}

	if string(out.Bytes()) != "0\n" {
		t.Errorf("User namespace not supported in your kernel")
		return
	}
}

func TestExecuteAssignment(t *testing.T) {
	sh := NewShell(false)
	sh.SetNashdPath(nashdPath)

	err := sh.ExecuteString("assignment", `
        name=i4k
        echo $name
        echo $path
        `)

	if err != nil {
		t.Error(err)
		return
	}

	err = sh.ExecuteString("list assignment", `
        name=(honda civic)
        echo $name
        `)

	if err != nil {
		t.Error(err)
		return
	}
}