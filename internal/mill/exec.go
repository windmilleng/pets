package mill

import (
	"fmt"
	"go/build"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"

	"github.com/google/skylark"
	"github.com/windmilleng/pets/internal/loader"
	"github.com/windmilleng/pets/internal/proc"
)

const Petsfile = "Petsfile"
const threadLocalSourceFile = "sourceFile"

type Petsitter struct {
	Stdout io.Writer
	Stderr io.Writer
	Runner proc.Runner
}

// ExecFile takes a Petsfile and parses it using the Skylark interpreter
func (p *Petsitter) ExecFile(file string) error {
	absFile, err := filepath.Abs(file)
	if err != nil {
		return err
	}

	thread := p.newThread(absFile)
	_, err = skylark.ExecFile(thread, file, nil, p.builtins())
	return err
}

func (p *Petsitter) newThread(file string) *skylark.Thread {
	thread := &skylark.Thread{
		Print: func(_ *skylark.Thread, msg string) {
			fmt.Fprintln(p.Stdout, msg)
		},
		Load: p.load,
	}
	thread.SetLocal(threadLocalSourceFile, file)
	return thread
}

func (p *Petsitter) builtins() skylark.StringDict {
	return skylark.StringDict{
		"run":   skylark.NewBuiltin("run", p.run),
		"start": skylark.NewBuiltin("start", p.start),
	}
}

func (p *Petsitter) run(t *skylark.Thread, fn *skylark.Builtin, args skylark.Tuple, kwargs []skylark.Tuple) (skylark.Value, error) {
	var cmdV skylark.Value

	if err := skylark.UnpackArgs("cmdV", args, kwargs,
		"cmdV", &cmdV,
	); err != nil {
		return nil, err
	}

	cmdArgs, err := argToCmd(fn, cmdV)
	if err != nil {
		return nil, err
	}

	cwd, _ := os.Getwd()
	if err := p.Runner.RunWithIO(cmdArgs, cwd, p.Stdout, p.Stderr); err != nil {
		return nil, err
	}

	return skylark.None, nil
}

func (p *Petsitter) start(t *skylark.Thread, fn *skylark.Builtin, args skylark.Tuple, kwargs []skylark.Tuple) (skylark.Value, error) {
	var cmdV skylark.Value
	var process proc.PetsCommand

	if err := skylark.UnpackArgs("cmdV", args, kwargs,
		"cmdV", &cmdV,
	); err != nil {
		return nil, err
	}

	cmdArgs, err := argToCmd(fn, cmdV)
	if err != nil {
		return nil, err
	}

	cwd, _ := os.Getwd()
	if process, err = p.Runner.StartWithIO(cmdArgs, cwd, p.Stdout, p.Stderr); err != nil {
		return nil, err
	}
	pr := process.Proc.Pid

	d := &skylark.Dict{}
	pid := skylark.String("pid")
	proc := skylark.MakeInt(pr)
	d.Set(pid, proc)
	return d, nil
}

func (p *Petsitter) load(t *skylark.Thread, module string) (skylark.StringDict, error) {
	url, err := url.Parse(module)
	if err != nil {
		return nil, err
	}

	switch url.Scheme {
	case "go-get":
		importPath := path.Join(url.Host, url.Path)
		if fmt.Sprintf("go-get://%s", importPath) != module {
			return nil, fmt.Errorf("go-get URLs may not contain query or fragment info")
		}

		// TODO(nick): Use the dir returned by LoadGoRepo to run PetsFile recursively
		dir, err := loader.LoadGoRepo(importPath, build.Default)
		if err != nil {
			return nil, fmt.Errorf("load: %v", err)
		}

		return p.execPetsFileAt(dir, true)
	case "":
		dir := filepath.Join(filepath.Dir(t.Local(threadLocalSourceFile).(string)), module)
		return p.execPetsFileAt(dir, false)
	default:
		return nil, fmt.Errorf("Unknown load() strategy: %s. Available load schemes: go-get", url.Scheme)
	}
}

func (p *Petsitter) execPetsFileAt(module string, isMissingOk bool) (skylark.StringDict, error) {
	result := map[string]skylark.Value{}
	result["dir"] = skylark.String(module)

	info, err := os.Stat(module)
	if err != nil {
		if os.IsNotExist(err) && isMissingOk {
			return skylark.StringDict(result), nil
		}
		return nil, err
	}

	// If the user tried to load a directory, check if that
	// directory has a Petsfile
	if info.Mode().IsDir() {
		module = path.Join(module, Petsfile)

		info, err = os.Stat(module)
		if err != nil {
			if os.IsNotExist(err) && isMissingOk {
				return skylark.StringDict(result), nil
			}
			return nil, err
		}
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("File %q should be a plaintext Petsfile", module)
	}

	// The most exciting part of the function is finally here! We have an executable
	// Petsfile, so run it and grab the globals.
	thread := p.newThread(module)
	globals, err := skylark.ExecFile(thread, module, nil, p.builtins())
	if err != nil {
		return nil, err
	}

	for key, val := range globals {
		result[key] = val
	}

	return skylark.StringDict(result), nil
}

func argToCmd(b *skylark.Builtin, argV skylark.Value) ([]string, error) {
	switch argV := argV.(type) {
	case skylark.String:
		return []string{"bash", "-c", string(argV)}, nil
	default:
		return nil, fmt.Errorf("%v expects a string or list of strings; got %T (%v)", b.Name(), argV, argV)
	}
}

func GetFilePath() string {
	cwd, _ := os.Getwd()

	return filepath.Join(cwd, Petsfile)
}
