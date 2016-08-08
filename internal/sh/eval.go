package sh

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/NeowayLabs/nash/ast"
	"github.com/NeowayLabs/nash/errors"
)

// evalConcat reveives the AST representation of a concatenation of objects and
// returns the string representation, or error.
func (sh *Shell) evalConcat(path *ast.Arg) (string, error) {
	var pathStr string

	concat := path.Concat()

	for i := 0; i < len(concat); i++ {
		part := concat[i]

		if part.IsConcat() {
			return "", errors.NewError("Nested concat is not allowed")
		}

		if part.IsVariable() {
			partValues, err := sh.evalVariable(part)

			if err != nil {
				return "", err
			}

			if partValues.Type() == ListType {
				return "", fmt.Errorf("Concat of list variables is not allowed: %s = %v", part.Value(), partValues)
			} else if partValues.Type() != StringType {
				return "", fmt.Errorf("Invalid concat element: %v", partValues)
			}

			pathStr += partValues.Str()
		} else if part.IsQuoted() || part.IsUnquoted() {
			pathStr += part.Value()
		} else if part.IsList() {
			return "", errors.NewError("Concat of lists is not allowed: %+v", part.List())
		} else {
			return "", fmt.Errorf("Invalid argument: %+v", part)
		}
	}

	return pathStr, nil
}

func (sh *Shell) executeNode(node ast.Node, builtin bool) (*Obj, error) {
	var (
		obj *Obj
		err error
	)

	sh.logf("Executing node: %v\n", node)

	switch node.Type() {
	case ast.NodeBuiltin:
		err = sh.executeBuiltin(node.(*ast.BuiltinNode))
	case ast.NodeImport:
		err = sh.executeImport(node.(*ast.ImportNode))
	case ast.NodeShowEnv:
		err = sh.executeShowEnv(node.(*ast.ShowEnvNode))
	case ast.NodeComment:
		// ignore
	case ast.NodeSetAssignment:
		err = sh.executeSetAssignment(node.(*ast.SetAssignmentNode))
	case ast.NodeAssignment:
		err = sh.executeAssignment(node.(*ast.AssignmentNode))
	case ast.NodeCmdAssignment:
		err = sh.executeCmdAssignment(node.(*ast.CmdAssignmentNode))
	case ast.NodeCommand:
		err = sh.executeCommand(node.(*ast.CommandNode))
	case ast.NodePipe:
		err = sh.executePipe(node.(*ast.PipeNode))
	case ast.NodeRfork:
		err = sh.executeRfork(node.(*ast.RforkNode))
	case ast.NodeCd:
		err = sh.executeCd(node.(*ast.CdNode), builtin)
	case ast.NodeIf:
		err = sh.executeIf(node.(*ast.IfNode))
	case ast.NodeFnDecl:
		err = sh.executeFnDecl(node.(*ast.FnDeclNode))
	case ast.NodeFnInv:
		// invocation ignoring output
		_, err = sh.executeFnInv(node.(*ast.FnInvNode))
	case ast.NodeFor:
		err = sh.executeFor(node.(*ast.ForNode))
	case ast.NodeBindFn:
		err = sh.executeBindFn(node.(*ast.BindFnNode))
	case ast.NodeDump:
		err = sh.executeDump(node.(*ast.DumpNode))
	case ast.NodeReturn:
		if sh.IsFn() {
			obj, err = sh.executeReturn(node.(*ast.ReturnNode))
		} else {
			err = errors.NewError("Unexpected return outside of function declaration.")
		}
	default:
		// should never get here
		return nil, errors.NewError("invalid node: %v.", node.Type())
	}

	return obj, err
}

// ExecuteTree evaluates the given tree
func (sh *Shell) ExecuteTree(tr *ast.Tree) (*Obj, error) {
	if tr == nil || tr.Root == nil {
		return nil, errors.NewError("nothing parsed")
	}

	root := tr.Root

	for _, node := range root.Nodes {
		obj, err := sh.executeNode(node, false)

		if err != nil {
			type IgnoreError interface {
				Ignore() bool
			}

			if errIgnore, ok := err.(IgnoreError); ok && errIgnore.Ignore() {
				continue
			}

			type InterruptedError interface {
				Interrupted() bool
			}

			if errInterrupted, ok := err.(InterruptedError); ok && errInterrupted.Interrupted() {
				return obj, err
			}

			return nil, err
		}

		if node.Type() == ast.NodeReturn {
			return obj, nil
		}
	}

	return nil, nil
}

func (sh *Shell) executeReturn(n *ast.ReturnNode) (*Obj, error) {
	if n.Return() == nil {
		return nil, nil
	}

	return sh.evalArg(n.Return())
}

func (sh *Shell) executeBuiltin(node *ast.BuiltinNode) error {
	// cd and for does not return data
	_, err := sh.executeNode(node.Stmt(), true)
	return err
}

func (sh *Shell) executeImport(node *ast.ImportNode) error {
	arg := node.Path()

	obj, err := sh.evalArg(arg)

	if err != nil {
		return err
	}

	if obj.Type() != StringType {
		return errors.NewError("Invalid type on import argument: %s", obj.Type())
	}

	fname := obj.Str()

	sh.logf("Importing '%s'", fname)

	if len(fname) > 0 && fname[0] == '/' {
		return sh.ExecuteFile(fname)
	}

	tries := make([]string, 0, 4)
	tries = append(tries, fname)

	if sh.currentFile != "" {
		tries = append(tries, path.Dir(sh.currentFile)+"/"+fname)
	}

	nashPath, ok := sh.Getenv("NASHPATH")

	if !ok {
		return errors.NewError("NASHPATH environment variable not set on shell %s", sh.name)
	} else if nashPath.Type() != StringType {
		return errors.NewError("NASHPATH must be n string")
	}

	dotDir := nashPath.String()

	tries = append(tries, dotDir+"/"+fname)
	tries = append(tries, dotDir+"/lib/"+fname)

	sh.logf("Trying %q\n", tries)

	for _, path := range tries {
		d, err := os.Stat(path)

		if err != nil {
			continue
		}

		if m := d.Mode(); !m.IsDir() {
			return sh.ExecuteFile(path)
		}
	}

	return errors.NewError("Failed to import path '%s'. The locations below have been tried:\n \"%s\"",
		fname,
		strings.Join(tries, `", "`))
}

func (sh *Shell) executeShowEnv(node *ast.ShowEnvNode) error {
	envVars := buildenv(sh.Environ())
	for _, e := range envVars {
		fmt.Fprintf(sh.stdout, "%s\n", e)
	}

	return nil
}

func (sh *Shell) executePipe(pipe *ast.PipeNode) error {
	var (
		closeFiles     []io.Closer
		closeAfterWait []io.Closer
		defStatus      = defStatusCode
		errIndex       int
		err            error
	)

	defer func() {
		for _, c := range closeAfterWait {
			c.Close()
		}
	}()

	nodeCommands := pipe.Commands()

	if len(nodeCommands) < 2 {
		return errors.NewError("Pipe requires at least two commands.")
	}

	cmds := make([]Runner, len(nodeCommands))
	errs := make([]string, len(nodeCommands))
	igns := make([]bool, len(nodeCommands)) // ignoreErrors
	cods := make([]string, len(nodeCommands))

	for i := 0; i < len(nodeCommands); i++ {
		errs[i] = "not started"
		cods[i] = strconv.Itoa(defStatus)
	}

	last := len(nodeCommands) - 1

	// Create all commands
	for i := 0; i < len(nodeCommands); i++ {
		var (
			cmd    Runner
			ignore bool
		)

		nodeCmd := nodeCommands[i]

		cmd, ignore, err = sh.getCommand(nodeCmd)

		igns[i] = ignore

		if err != nil {
			errIndex = i
			goto pipeError
		}

		err = cmd.SetArgs(nodeCmd.Args(), sh)

		if err != nil {
			errIndex = i
			goto pipeError
		}

		cmd.SetStdin(sh.stdin)
		cmd.SetStderr(sh.stderr)

		if i < last {
			closeFiles, err = sh.setRedirects(cmd, nodeCmd.Redirects())
			closeAfterWait = append(closeAfterWait, closeFiles...)

			if err != nil {
				errIndex = i
				goto pipeError
			}
		}

		cmds[i] = cmd
	}

	// Shell does not support stdin redirection yet
	cmds[0].SetStdin(sh.stdin)

	// Setup the commands. Pointing the stdin of next command to stdout of previous.
	// Except the last one
	for i, cmd := range cmds[:last] {
		var (
			stdin io.ReadCloser
		)

		cmd.SetStderr(sh.stderr)

		stdin, err = cmd.StdoutPipe()

		if err != nil {
			errIndex = i
			goto pipeError
		}

		cmds[i+1].SetStdin(stdin)
	}

	cmds[last].SetStdout(sh.stdout)
	cmds[last].SetStderr(sh.stderr)

	closeFiles, err = sh.setRedirects(cmds[last], nodeCommands[last].Redirects())
	closeAfterWait = append(closeAfterWait, closeFiles...)

	if err != nil {
		errIndex = last
		goto pipeError
	}

	for i := 0; i < len(cmds); i++ {
		cmd := cmds[i]

		err = cmd.Start()

		if err != nil {
			errIndex = i
			goto pipeError
		}

		errs[i] = "ok"
		cods[i] = "0"
	}

	for i, cmd := range cmds {
		err = cmd.Wait()

		if err != nil {
			errIndex = i
			goto pipeError
		}

		errs[i] = "ok"
		cods[i] = "0"
	}

	sh.Setvar("status", NewStrObj("0"))
	return nil

pipeError:
	if igns[errIndex] {
		errs[errIndex] = "none"
	} else {
		errs[errIndex] = err.Error()
	}

	cods[errIndex] = getErrStatus(err, defStatus)

	err = errors.NewError(strings.Join(errs, "|"))
	sh.Setvar("status", NewStrObj(strings.Join(cods, "|")))

	if igns[errIndex] {
		return nil
	}

	return err
}

func (sh *Shell) openRedirectLocation(location *ast.Arg) (io.WriteCloser, error) {
	var (
		protocol, locationStr string
	)

	if !location.IsVariable() && !location.IsQuoted() && !location.IsUnquoted() {
		return nil, errors.NewError("Invalid argument of type %v in redirection", location.ArgType())
	}

	if location.IsQuoted() || location.IsUnquoted() {
		locationStr = location.Value()
	} else {
		obj, err := sh.evalVariable(location)

		if err != nil {
			return nil, err
		}

		if obj.Type() != StringType {
			return nil, errors.NewError("Invalid object type in redirection: %+v", obj.Type())
		}

		locationStr = obj.Str()
	}

	if len(locationStr) > 6 {
		if locationStr[0:6] == "tcp://" {
			protocol = "tcp"
		} else if locationStr[0:6] == "udp://" {
			protocol = "udp"
		} else if len(locationStr) > 7 && locationStr[0:7] == "unix://" {
			protocol = "unix"
		}
	}

	if protocol == "" {
		return os.OpenFile(locationStr, os.O_RDWR|os.O_CREATE, 0644)
	}

	switch protocol {
	case "tcp", "udp":
		netParts := strings.Split(locationStr[6:], ":")

		if len(netParts) != 2 {
			return nil, errors.NewError("Invalid tcp/udp address: %s", locationStr)
		}

		url := netParts[0] + ":" + netParts[1]

		return net.Dial(protocol, url)
	case "unix":
		return net.Dial(protocol, locationStr[7:])
	}

	return nil, errors.NewError("Unexpected redirection value: %s", locationStr)
}

func (sh *Shell) setRedirects(cmd Runner, redirDecls []*ast.RedirectNode) ([]io.Closer, error) {
	var closeAfterWait []io.Closer

	for _, r := range redirDecls {
		closeFiles, err := sh.buildRedirect(cmd, r)
		closeAfterWait = append(closeAfterWait, closeFiles...)

		if err != nil {
			return closeAfterWait, err
		}
	}

	return closeAfterWait, nil
}

func (sh *Shell) buildRedirect(cmd Runner, redirDecl *ast.RedirectNode) ([]io.Closer, error) {
	var closeAfterWait []io.Closer

	if redirDecl.LeftFD() > 2 || redirDecl.LeftFD() < ast.RedirMapSupress {
		return closeAfterWait, errors.NewError("Invalid file descriptor redirection: fd=%d", redirDecl.LeftFD())
	}

	if redirDecl.RightFD() > 2 || redirDecl.RightFD() < ast.RedirMapSupress {
		return closeAfterWait, errors.NewError("Invalid file descriptor redirection: fd=%d", redirDecl.RightFD())
	}

	var err error

	// Note(i4k): We need to remove the repetitive code in some smarter way
	switch redirDecl.LeftFD() {
	case 0:
		return closeAfterWait, fmt.Errorf("Does not support stdin redirection yet")
	case 1:
		switch redirDecl.RightFD() {
		case 0:
			return closeAfterWait, errors.NewError("Invalid redirect mapping: %d -> %d", 1, 0)
		case 1: // do nothing
		case 2:
			cmd.SetStdout(cmd.Stderr())
		case ast.RedirMapNoValue:
			if redirDecl.Location() == nil {
				return closeAfterWait, errors.NewError("Missing file in redirection: >[%d] <??>", redirDecl.LeftFD())
			}

			file, err := sh.openRedirectLocation(redirDecl.Location())

			if err != nil {
				return closeAfterWait, err
			}

			cmd.SetStdout(file)
			closeAfterWait = append(closeAfterWait, file)
		case ast.RedirMapSupress:
			file, err := os.OpenFile("/dev/null", os.O_RDWR, 0644)

			if err != nil {
				return closeAfterWait, err
			}

			cmd.SetStdout(file)
		}
	case 2:
		switch redirDecl.RightFD() {
		case 0:
			return closeAfterWait, errors.NewError("Invalid redirect mapping: %d -> %d", 2, 1)
		case 1:
			cmd.SetStderr(cmd.Stdout())
		case 2: // do nothing
		case ast.RedirMapNoValue:
			if redirDecl.Location() == nil {
				return closeAfterWait, errors.NewError("Missing file in redirection: >[%d] <??>", redirDecl.LeftFD())
			}

			file, err := sh.openRedirectLocation(redirDecl.Location())

			if err != nil {
				return closeAfterWait, err
			}

			cmd.SetStderr(file)
			closeAfterWait = append(closeAfterWait, file)
		case ast.RedirMapSupress:
			file, err := os.OpenFile("/dev/null", os.O_RDWR, 0644)

			if err != nil {
				return closeAfterWait, err
			}

			cmd.SetStderr(file)
		}
	case ast.RedirMapNoValue:
		if redirDecl.Location() == nil {
			return closeAfterWait, errors.NewError("Missing file in redirection: >[%d] <??>", redirDecl.LeftFD())
		}

		file, err := sh.openRedirectLocation(redirDecl.Location())

		if err != nil {
			return closeAfterWait, err
		}

		cmd.SetStdout(file)
		closeAfterWait = append(closeAfterWait, file)
	}

	return closeAfterWait, err
}

func (sh *Shell) getCommand(c *ast.CommandNode) (Runner, bool, error) {
	var (
		ignoreError bool
		cmd         Runner
		err         error
	)

	cmdName := c.Name()

	sh.logf("Executing: %s\n", c.Name())

	if len(cmdName) > 1 && cmdName[0] == '-' {
		ignoreError = true
		cmdName = cmdName[1:]

		sh.logf("Ignoring error\n")
	}

	cmd, err = NewCmd(cmdName)

	if err != nil {
		type NotFound interface {
			NotFound() bool
		}

		sh.logf("Command fails: %s", err.Error())

		if errNotFound, ok := err.(NotFound); ok && errNotFound.NotFound() {
			if fn, ok := sh.Getbindfn(cmdName); ok {
				sh.logf("Executing bind %s", cmdName)

				if len(c.Args()) > len(fn.argNames) {
					err = errors.NewError("Too much arguments for"+
						" function '%s'. It expects %d args, but given %d. Arguments: %q",
						fn.name,
						len(fn.argNames),
						len(c.Args()), c.Args())
					return nil, ignoreError, err
				}

				for i := 0 + len(c.Args()); i < len(fn.argNames); i++ {
					c.SetArgs(append(c.Args(), ast.NewArg(0, ast.ArgQuoted)))
				}

				return fn, ignoreError, nil
			}

			return nil, ignoreError, err
		}

		return nil, ignoreError, err
	}

	return cmd, ignoreError, nil
}

func (sh *Shell) executeCommand(c *ast.CommandNode) error {
	var (
		ignoreError    bool
		status         = 127
		envVars        []string
		closeAfterWait []io.Closer
		cmd            Runner
		err            error
	)

	defer func() {
		for _, c := range closeAfterWait {
			c.Close()
		}
	}()

	cmd, ignoreError, err = sh.getCommand(c)

	if err != nil {
		goto cmdError
	}

	err = cmd.SetArgs(c.Args(), sh)

	if err != nil {
		goto cmdError
	}

	envVars = buildenv(sh.Environ())

	cmd.SetEnviron(envVars)

	cmd.SetStdin(sh.stdin)
	cmd.SetStdout(sh.stdout)
	cmd.SetStderr(sh.stderr)

	closeAfterWait, err = sh.setRedirects(cmd, c.Redirects())

	if err != nil {
		goto cmdError
	}

	err = cmd.Start()

	if err != nil {
		goto cmdError
	}

	err = cmd.Wait()

	if err != nil {
		goto cmdError
	}

	sh.Setvar("status", NewStrObj("0"))

	return nil

cmdError:
	sh.Setvar("status", NewStrObj(getErrStatus(err, status)))

	if ignoreError {
		return newErrIgnore(err.Error())
	}

	return err
}

func (sh *Shell) evalVariable(a *ast.Arg) (*Obj, error) {
	var (
		v  *Obj
		ok bool
	)

	if a.ArgType() != ast.ArgVariable {
		return nil, errors.NewError("Invalid eval of non variable argument: %s", a)
	}

	varName := a.Value()

	if v, ok = sh.GetVar(varName[1:]); !ok {
		return nil, fmt.Errorf("Variable %s not set on shell %s", varName, sh.name)
	}

	if a.Index() != nil {
		if v.Type() != ListType {
			return nil, errors.NewError("Invalid indexing of non-list variable: %s", v.Type())
		}

		var (
			indexNum int
			err      error
		)

		idxArg := a.Index()

		if idxArg.ArgType() == ast.ArgNumber {
			indexNum, err = strconv.Atoi(idxArg.Value())

			if err != nil {
				return nil, err
			}
		} else if idxArg.ArgType() == ast.ArgVariable {
			idxObj, err := sh.evalVariable(idxArg)

			if err != nil {
				return nil, err
			}

			if idxObj.Type() != StringType {
				return nil, errors.NewError("Invalid object type on index value: %s", idxObj.Type())
			}

			idxVal := idxObj.Str()
			indexNum, err = strconv.Atoi(idxVal)

			if err != nil {
				return nil, err
			}
		}

		values := v.List()

		if indexNum < 0 || indexNum >= len(values) {
			return nil, errors.NewError("Index out of bounds. len(%s) == %d, but given %d", varName, len(values), indexNum)
		}

		value := values[indexNum]
		return NewStrObj(value), nil
	}

	return v, nil
}

func (sh *Shell) evalArg(arg *ast.Arg) (*Obj, error) {
	if arg.IsQuoted() || arg.IsUnquoted() {
		return NewStrObj(arg.Value()), nil
	} else if arg.IsConcat() {
		argVal, err := sh.evalConcat(arg)

		if err != nil {
			return nil, err
		}

		return NewStrObj(argVal), nil
	} else if arg.IsVariable() {
		obj, err := sh.evalVariable(arg)

		if err != nil {
			return nil, err
		}

		return obj, nil
	} else if arg.IsList() {
		argList := arg.List()
		values := make([]string, 0, len(argList))

		for _, arg := range argList {
			obj, err := sh.evalArg(arg)

			if err != nil {
				return nil, err
			}

			if obj.Type() != StringType {
				return nil, errors.NewError("Nested lists are not supported")
			}

			values = append(values, obj.Str())
		}

		return NewListObj(values), nil
	}

	return nil, errors.NewError("Invalid argument type: %+v", arg)
}

func (sh *Shell) executeSetAssignment(v *ast.SetAssignmentNode) error {
	var (
		varValue *Obj
		ok       bool
	)

	varName := v.Identifier()

	if varValue, ok = sh.GetVar(varName); !ok {
		return fmt.Errorf("Variable '%s' not set on shell %s", varName, sh.name)
	}

	sh.Setenv(varName, varValue)

	return nil
}

func (sh *Shell) concatElements(elem *ast.Arg) (string, error) {
	value := ""

	concat := elem.Concat()
	for i := 0; i < len(concat); i++ {
		ec := concat[i]

		obj, err := sh.evalArg(ec)

		if err != nil {
			return "", err
		}

		if obj.Type() != StringType {
			return "", errors.NewError("Impossible to concat elements of type %s", obj.Type())
		}

		value = value + obj.String()
	}

	return value, nil
}

func (sh *Shell) executeCmdAssignment(v *ast.CmdAssignmentNode) error {
	var (
		varOut bytes.Buffer
		err    error
	)

	bkStdout := sh.stdout

	sh.SetStdout(&varOut)

	defer sh.SetStdout(bkStdout)

	assign := v.Command()

	switch assign.Type() {
	case ast.NodeCommand:
		err = sh.executeCommand(assign.(*ast.CommandNode))
	case ast.NodePipe:
		err = sh.executePipe(assign.(*ast.PipeNode))
	case ast.NodeFnInv:
		fnValues, err := sh.executeFnInv(assign.(*ast.FnInvNode))

		if err != nil {
			return err
		}

		sh.Setvar(v.Name(), fnValues)
		return nil
	default:
		err = errors.NewError("Unexpected node in assignment: %s", assign.String())
	}

	if err != nil {
		return err
	}

	var strelems []string

	outStr := string(varOut.Bytes())

	if ifs, ok := sh.GetVar("IFS"); ok && ifs.Type() == ListType {
		strelems = strings.FieldsFunc(outStr, func(r rune) bool {
			for _, delim := range ifs.List() {
				if len(delim) > 0 && rune(delim[0]) == r {
					return true
				}
			}

			return false
		})

		sh.Setvar(v.Name(), NewListObj(strelems))
	} else {
		sh.Setvar(v.Name(), NewStrObj(outStr))
	}

	return nil
}

func (sh *Shell) executeAssignment(v *ast.AssignmentNode) error {
	var err error

	obj, err := sh.evalArg(v.Value())

	if err != nil {
		return err
	}

	sh.Setvar(v.Identifier(), obj)
	return nil
}

func (sh *Shell) executeBuiltinCd(cd *ast.CdNode) error {
	var (
		pathlist []string
		pathStr  string
	)

	path := cd.Dir()

	if path == nil {
		pathobj, ok := sh.Getenv("HOME")

		if !ok {
			return errors.NewError("Nash don't know where to cd. No variable $HOME set")
		}

		if pathobj.Type() != StringType {
			return fmt.Errorf("Invalid $HOME value: %v", pathlist)
		}

		pathStr = pathobj.Str()
	} else {
		obj, err := sh.evalArg(path)

		if err != nil {
			return err
		}

		if obj.Type() != StringType {
			return errors.NewError("HOME variable has invalid type: %s", obj.Type())
		}

		pathStr = obj.Str()
	}

	err := os.Chdir(pathStr)

	if err != nil {
		return err
	}

	pwd, ok := sh.GetVar("PWD")

	if !ok {
		return fmt.Errorf("Variable $PWD is not set on shell %s", sh.name)
	}

	cpwd := NewStrObj(pathStr)

	sh.Setvar("OLDPWD", pwd)
	sh.Setvar("PWD", cpwd)
	sh.Setenv("OLDPWD", pwd)
	sh.Setenv("PWD", cpwd)

	return nil
}

func (sh *Shell) executeCd(cd *ast.CdNode, builtin bool) error {
	var (
		cdAlias  *Shell
		hasAlias bool
	)

	if cdAlias, hasAlias = sh.Getbindfn("cd"); !hasAlias || builtin {
		return sh.executeBuiltinCd(cd)
	}

	path := cd.Dir()

	args := make([]*ast.Arg, 0, 1)

	if path != nil {
		args = append(args, path)
	} else {
		// empty arg
		args = append(args, ast.NewArg(0, ast.ArgQuoted))
	}

	_, err := sh.executeFn(cdAlias, args)
	return err
}

func (sh *Shell) evalIfArguments(n *ast.IfNode) (string, string, error) {
	lvalue := n.Lvalue()
	rvalue := n.Rvalue()

	lobj, err := sh.evalArg(lvalue)

	if err != nil {
		return "", "", err
	}

	robj, err := sh.evalArg(rvalue)

	if err != nil {
		return "", "", err
	}

	if lobj.Type() != StringType {
		return "", "", errors.NewError("lvalue is not comparable.")
	}

	if robj.Type() != StringType {
		return "", "", errors.NewError("rvalue is not comparable")
	}

	return lobj.Str(), robj.Str(), nil
}

func (sh *Shell) executeIfEqual(n *ast.IfNode) error {
	lstr, rstr, err := sh.evalIfArguments(n)

	if err != nil {
		return err
	}

	if lstr == rstr {
		_, err = sh.ExecuteTree(n.IfTree())
		return err
	} else if n.ElseTree() != nil {
		_, err = sh.ExecuteTree(n.ElseTree())
		return err
	}

	return nil
}

func (sh *Shell) executeIfNotEqual(n *ast.IfNode) error {
	lstr, rstr, err := sh.evalIfArguments(n)

	if err != nil {
		return err
	}

	if lstr != rstr {
		_, err = sh.ExecuteTree(n.IfTree())
		return err
	} else if n.ElseTree() != nil {
		_, err = sh.ExecuteTree(n.ElseTree())
		return err
	}

	return nil
}

func (sh *Shell) executeFn(fn *Shell, args []*ast.Arg) (*Obj, error) {
	err := fn.SetArgs(args, sh)

	if err != nil {
		return nil, err
	}

	return fn.Execute()
}

func (sh *Shell) executeFnInv(n *ast.FnInvNode) (*Obj, error) {
	var (
		fn *Shell
		ok bool
	)

	fnName := n.Name()

	if len(fnName) > 0 && fnName[0] == '$' {
		argVar := ast.NewArg(n.Position(), ast.ArgVariable)
		argVar.SetString(fnName)

		obj, err := sh.evalVariable(argVar)

		if err != nil {
			return nil, err
		}

		if obj.Type() != FnType {
			return nil, errors.NewError("Variable '%s' isnt a function.", fnName)
		}

		fn = obj.Fn()
	} else {
		fn, ok = sh.GetFn(fnName)

		if !ok {
			return nil, errors.NewError("no such function '%s'", fnName)
		}
	}

	err := fn.SetArgs(n.Args(), sh)

	if err != nil {
		return nil, err
	}

	return fn.Execute()
}

func (sh *Shell) executeInfLoop(tr *ast.Tree) error {
	var err error

	for {
		_, err = sh.ExecuteTree(tr)

		type interruptedError interface {
			Interrupted() bool
		}

		if errInterrupted, ok := err.(interruptedError); ok && errInterrupted.Interrupted() {
			break
		}

		sh.Lock()

		if sh.getIntr() {
			sh.setIntr(false)

			if err != nil {
				err = newErrInterrupted(err.Error())
			} else {
				err = newErrInterrupted("loop interrupted")
			}
		}

		sh.Unlock()

		if err != nil {
			break
		}
	}

	return err
}

func (sh *Shell) executeFor(n *ast.ForNode) error {
	sh.Lock()
	sh.looping = true
	sh.Unlock()

	defer func() {
		sh.Lock()
		defer sh.Unlock()

		sh.looping = false
	}()

	if n.InVar() == "" {
		return sh.executeInfLoop(n.Tree())
	}

	id := n.Identifier()
	inVar := n.InVar()

	argVar := ast.NewArg(n.Position(), ast.ArgVariable)
	argVar.SetString(inVar)

	obj, err := sh.evalVariable(argVar)

	if err != nil {
		return err
	}

	if obj.Type() != ListType {
		return errors.NewError("Invalid variable type in for range: %s", obj.Type())
	}

	for _, val := range obj.List() {
		sh.Setvar(id, NewStrObj(val))

		_, err = sh.ExecuteTree(n.Tree())

		type interruptedError interface {
			Interrupted() bool
		}

		if errInterrupted, ok := err.(interruptedError); ok && errInterrupted.Interrupted() {
			return err
		}

		sh.Lock()

		if sh.getIntr() {
			sh.setIntr(false)
			sh.Unlock()

			if err != nil {
				return newErrInterrupted(err.Error())
			}

			return newErrInterrupted("loop interrupted")
		}

		sh.Unlock()

		if err != nil {
			return err
		}
	}

	return nil
}

func (sh *Shell) executeFnDecl(n *ast.FnDeclNode) error {
	fn, err := NewSubShell(n.Name(), sh)

	if err != nil {
		return err
	}

	fn.SetDebug(sh.debug)
	fn.SetStdout(sh.stdout)
	fn.SetStderr(sh.stderr)
	fn.SetStdin(sh.stdin)
	fn.SetRepr(n.String())

	args := n.Args()

	for i := 0; i < len(args); i++ {
		arg := args[i]

		fn.AddArgName(arg)
	}

	fn.SetTree(n.Tree())

	fnName := n.Name()

	if fnName == "" {
		fnName = fmt.Sprintf("lambda %d", int(sh.lambdas))
		sh.lambdas++
	}

	sh.fns[fnName] = fn

	sh.Setvar(fnName, NewFnObj(fn))
	sh.logf("Function %s declared on '%s'", fnName, sh.name)

	return nil
}

func (sh *Shell) dumpVar(file io.Writer) {
	for n, v := range sh.vars {
		printVar(file, n, v)
	}
}

func (sh *Shell) dumpEnv(file io.Writer) {
	for n := range sh.env {
		printEnv(file, n)
	}
}

func (sh *Shell) dumpFns(file io.Writer) {
	for _, f := range sh.fns {
		fmt.Fprintf(file, "%s\n\n", f.String())
	}
}

func (sh *Shell) dump(out io.Writer) {
	sh.dumpVar(out)
	sh.dumpEnv(out)
	sh.dumpFns(out)
}

func (sh *Shell) executeDump(n *ast.DumpNode) error {
	var (
		err  error
		file io.Writer
		obj  *Obj
	)

	fnameArg := n.Filename()

	if fnameArg == nil {
		file = sh.stdout
		goto execDump
	}

	obj, err = sh.evalArg(fnameArg)

	if err != nil {
		return err
	}

	if obj.Type() != StringType {
		return errors.NewError("dump does not support argument of type %s", obj.Type())
	}

	file, err = os.OpenFile(obj.Str(), os.O_CREATE|os.O_RDWR, 0644)

	if err != nil {
		return err
	}

execDump:
	sh.dump(file)

	return nil
}

func (sh *Shell) executeBindFn(n *ast.BindFnNode) error {
	if fn, ok := sh.GetFn(n.Name()); ok {
		sh.Setbindfn(n.CmdName(), fn)
	} else {
		return errors.NewError("No such function '%s'", n.Name())
	}

	return nil
}

func (sh *Shell) executeIf(n *ast.IfNode) error {
	op := n.Op()

	if op == "==" {
		return sh.executeIfEqual(n)
	} else if op == "!=" {
		return sh.executeIfNotEqual(n)
	}

	return fmt.Errorf("Invalid operation '%s'.", op)
}

func nashdAutoDiscover() string {
	path, err := os.Readlink("/proc/self/exe")

	if err != nil {
		path = os.Args[0]

		if _, err := os.Stat(path); err != nil {
			return ""
		}
	}

	return path
}
