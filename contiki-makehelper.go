/*
 * Contiki-MakeHelper is free software; you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation; either version 2 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, write to the Free Software
 * Foundation, Inc., 51 Franklin St, Fifth Floor, Boston, MA  02110-1301  USA
 *
 * As a special exception, you may use this file as part of a free software
 * library without restriction.  Specifically, if other files instantiate
 * templates or use macros or inline functions from this file, or you compile
 * this file and link it with other files to produce an executable, this
 * file does not by itself cause the resulting executable to be covered by
 * the GNU General Public License.  This exception does not however
 * invalidate any other reasons why the executable file might be covered by
 * the GNU General Public License.
 *
 * Copyright 2016 TOKITA Hiroshi
 */

 package main

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type Command struct {
	Stage string `json:"stage"`
	Recipe string `json:"recipe"`
	Source string `json:"source"`
	Target string `json:"target"`
	Flags []string `json:"flags"`
	BuildPath string `json:"build_path"`
	CorePath string `json:"core_path"`
	SystemPath string `json:"system_path"`
	VariantPath string `json:"variant_path"`
	ProjectName string `json:"project_name"`
	ArchiveFile string `json:"archive_file"`
}

type Submodule struct {
	Path string `json:"path"`
	Url  string `json:"url"`
	Revision string `json:"revision"`
}

func Verbose(level int, format string, args ...interface{}) {
	if(level <= verbose) {
		fmt.Fprintf(os.Stderr, format, args...);
	}
}

func write_file(file string, buf []byte) (int, error) {
	fp, err := os.OpenFile(file, syscall.O_RDWR | syscall.O_CREAT, os.ModeExclusive)
	if err != nil { return 0, err }
	defer fp.Close()

	n, err := fp.Write(buf)
	if err != nil { return 0, err }

	return n, nil
}

func encode_to_file(file string, ifc interface{}) (int, error) {
	buf, err := json.MarshalIndent(ifc, "", " ")
	if err != nil { return 0, err }
	return write_file(file, buf)
}

func decode_from_file(file string, ifc interface{}) error {
	_, err := os.Stat(file)
	if os.IsNotExist(err) { return err }

	fp, err := os.Open(file)
	if err != nil { return err }
	defer fp.Close()

	r := bufio.NewReader(fp)
	dec := json.NewDecoder(r)
	dec.Decode(&ifc)

	Verbose(5, "decode_from_file: %v\n", ifc)
	return nil
}

func select_command(slc []Command, f func(s Command) bool) []Command {
    ans := make([]Command, 0)
    for _, x := range slc {
        if f(x) == true {
            ans = append(ans, x)
        }
    }
    return ans
}

func collect_string(slc []Command, f func(s Command) string) []string {
    ans := make([]string, 0)
    for _, x := range slc {
	ans = append(ans, f(x))
    }
    return ans
}

func contains(s []string, e string) bool {
    for _, a := range s {
        if a == e {
            return true
        }
    }
    return false
}

func format_makefile(template string, replace map[string]string) string {
	Verbose(3, "template: %v\n", template)
	Verbose(3, "replace: %v\n", replace)

	out := ""
	_, err := os.Stat(template)
	if os.IsNotExist(err) { errors.New(err.Error() ) }

	fp, err := os.Open(template)
	if err != nil { errors.New(err.Error() ) }
	defer fp.Close()

	scanner := bufio.NewScanner(fp)
	for scanner.Scan() {
		line := scanner.Text()
		rep := regexp.MustCompile(`(###\s*<<<)([^>\s]*)(>>>\s*###)`)
		matches := rep.FindAllStringSubmatch(line,-1)

		if matches != nil {
			found := false

			for k := range replace {
				if(k == matches[0][2]) {
					out = out + rep.ReplaceAllString(line, replace[k]) + "\n"
					found = true
				}
			}
			if(found) {
				continue
			}
		}
		out = out + line + "\n"
	}
	if err := scanner.Err(); err != nil {
		panic(err)
	}

	Verbose(5, out)
	return out
}

func ToMsysSlash(p string) string {
	s := filepath.ToSlash(p)
	if len(s) < 4 {
		return s
	}
	if (s[1:3] == ":/") {
		ss := "/"
		ss += s[0:1]
		ss += s[2:]
		return ss
	}
	return s
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			log.Fatal(err)
			return err
		}
		defer rc.Close()

		fnsplit:= strings.Split(f.Name, "/")
		fnjoin := filepath.Join(fnsplit[1:]...)

		path := filepath.Join(dest, fnjoin)

		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.Mode())
			continue
		}

		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			log.Fatal(err)
			return err
		}
		defer f.Close()

		Verbose(3, "extact %s\n", path);
		_, err = io.Copy(f, rc)
		if err != nil {
			log.Fatal(err)
			return err
		}
	}

	return nil
}

func PreparePackage(pfpath string) error {

	mods := []Submodule{}

	decode_from_file(filepath.Join(pfpath, "dist", "submodules.json"), &mods)
	Verbose(3, "decode_from_file: %v\n", mods)

	for _, mod := range mods {

		pfmodpath := filepath.Join(filepath.FromSlash(pfpath), filepath.FromSlash(mod.Path) )
		infos, err := ioutil.ReadDir(pfmodpath)

		if err != nil { return err; }

		for _, ifs := range infos {
			Verbose(3, "%s\n", ifs.Name() )
		}

		if len(infos) != 0 {
			Verbose(3, "%s are exists.\n", pfmodpath)
			continue
		}

		Verbose(0, "Preparing package ...\n")

		url := mod.Url + "/archive/" + mod.Revision + ".zip"

		Verbose(0, "Download %s ...\n", url)

		client := http.Client{}

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			log.Fatal(err)
			errors.New(err.Error() )
		}

		resp, err := client.Do(req)
		if err != nil {
			log.Fatal(err)
			errors.New(err.Error() )
		}

		defer resp.Body.Close()

		body, err := ioutil.ReadAll(resp.Body);
		if err != nil {
			log.Fatal(err)
			errors.New(err.Error() )
		}

		tmpfile, err := ioutil.TempFile(os.TempDir(), "contiki-makehelper")
		if err != nil {
			log.Fatal(err)
			errors.New(err.Error() )
		}
		defer os.Remove(tmpfile.Name() )

		tmpfile.Write(body)
		tmpfile.Close()

		Verbose(0, "Download complete.\n")

		Verbose(0, "Extract to ...\n", filepath.Join(pfpath, mod.Path) )
		err = unzip(tmpfile.Name(), filepath.Join(pfpath, mod.Path) )
		if err != nil {
			log.Fatal(err)
			errors.New(err.Error() )
		}
		Verbose(0, "Extract complete.\n")
	}

	return nil
}

func ExecCommand(exe string, args... string) int {
	path := os.Getenv("PATH")
	if(cmds_path != "") {
		path = cmds_path + string(os.PathListSeparator) + path
	}
	if(compiler_path != "") {
		path = compiler_path + string(os.PathListSeparator) + path
	}
	if(uploader_path != "") {
		path = uploader_path + string(os.PathListSeparator) + path
	}
	os.Setenv("PATH", path)
	os.Setenv("LANG", "C")


	path = os.Getenv("PATH")
	Verbose(1, "PATH=" + path + "\n")
	Verbose(0, exe + " " + strings.Join(args, " ") + "\n")

	cmd := exec.Command(exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()

	var exitStatus int
	if err != nil {
		if e2, ok := err.(*exec.ExitError); ok {
			if s, ok := e2.Sys().(syscall.WaitStatus); ok {
				exitStatus = s.ExitStatus()
			} else {
				panic(errors.New("Unimplemented for system where exec.ExitError.Sys() is not syscall.WaitStatus."))
			}
		}
	} else {
		exitStatus = 0
	}

	if exitStatus != 0 || verbose > 3 {
		Verbose(0, "exec.Command.Run err=%d\n", exitStatus)
	}

	return exitStatus;
}

var (
	recipe string
	source string
	target string
	build_path string
	core_path string
	system_path string
	variant_path string
	platform_path string
	project_name string
	archive_file string
	stage string
	template string
	variant_name string
	platform_version string

	contiki_target_main string
	cmds_path string
	compiler_path string
	uploader_path string
	includes string
	verbose int
)

func main() {

	flag.StringVar(&build_path,		"build.path", "",		"same as platform.txt")
	flag.StringVar(&core_path,		"build.core.path", "",		"same as platform.txt")
	flag.StringVar(&system_path,		"build.system.path", "",	"same as platform.txt")
	flag.StringVar(&variant_path,		"build.variant.path", "",	"same as platform.txt")
	flag.StringVar(&platform_path,		"runtime.platform.path", "",	"same as platform.txt")
	flag.StringVar(&variant_name,		"build.variant", "",		"same as platform.txt")
	flag.StringVar(&project_name,		"project_name", "",		"same as platform.txt")
	flag.StringVar(&archive_file,		"archive_file", "",		"same as platform.txt")
	flag.StringVar(&recipe,			"recipe", "",			"recipe")
	flag.StringVar(&stage,			"stage", "",			"build stage")
	flag.StringVar(&target,			"target", "",			"target file")
	flag.StringVar(&source,			"source", "",			"source file")
	flag.StringVar(&template,		"template", "",			"Makefile template")
	flag.StringVar(&cmds_path,		"build.usr.bin.path", "",	"target object")
	flag.StringVar(&compiler_path,		"build.compiler.path", "",	"cpmpiler path")
	flag.StringVar(&uploader_path,		"build.uploader.path", "",	"uploader path")
	flag.StringVar(&contiki_target_main,	"contiki.target.main", "",	"CONTIKI_TARGET_MAIN")
	flag.StringVar(&platform_version,	"platform.version", "",		"version")
	flag.StringVar(&includes,		"includes", "",			"includes")
	flag.IntVar(&verbose,			"verbose", 0,			"verbose level")
	flag.Parse()

	flags := flag.Args()

	Verbose(3, "recipe:%s stage:%s target:%s source:%s\n", recipe,stage,target,source)

	genmf := strings.Replace(target + "_" + source, "\\", "_", -1);
	genmf = strings.Replace(genmf, "/", "_", -1);
	genmf = strings.Replace(genmf, ":", "_", -1);
	genmf = build_path + string(os.PathSeparator) + genmf + "." + recipe + ".genmf"

	if recipe == "cpp.o" || recipe == "c.o" || recipe == "S.o" || recipe == "ar" || recipe == "ld" {
		cmd := Command{	stage, recipe, source, target, flags,
				build_path, core_path, system_path, variant_path,
				project_name, archive_file }

		stgfile := build_path + string(os.PathSeparator) + "genmf.stage"
		_, err := os.Stat(stgfile)
		if !os.IsNotExist(err) {
			decode_from_file(stgfile, &stage);
			cmd.Stage = stage
		}

		_, err = encode_to_file(genmf, cmd)
		if err != nil { errors.New(err.Error() ) }

	} else if recipe == "stage" {
		genmf = build_path + string(os.PathSeparator) + "genmf.stage"

		_, err := encode_to_file(genmf, stage)
		if err != nil { errors.New(err.Error() ) }

	} else if recipe == "echo" {
		fmt.Println(strings.Join(flags, " ") )

	} else if recipe == "make" {
		numcores := os.Getenv("NUMBER_OF_PROCESSORS")

		args := append([]string{ "-j" + numcores, "-C",ToMsysSlash(build_path)}, flags...)

		exitStatus := ExecCommand("make", args...)
		os.Exit(exitStatus)

	} else if recipe == "preproc.includes" || recipe == "preproc.macros" {

		err := PreparePackage(platform_path)
		if err != nil {
			panic(err)
		}

		args := append([]string{ "-s", "-C", ToMsysSlash(build_path)})

		includes := []string{}

		flaggroup := ""
		for _, f := range flags {
			if f == "-includes" || f == "-make-args" {
				flaggroup = f
				continue
			}

			if flaggroup == "-make-args" {
				args = append(args, f)
				continue
			} else if flaggroup == "-includes" {
				if strings.HasPrefix(f,"-I") {
					f = "-I" + ToMsysSlash(f[2:])
				}
				includes = append(includes, f)
			}
			includes = append(includes, f)
		}

		args = append(args, recipe)

		replace_map := map[string]string {}

		preprocfile := build_path + string(os.PathSeparator) + "genmf.preproc"
		_, err = os.Stat(preprocfile)
		if !os.IsNotExist(err) {
			decode_from_file(preprocfile, &replace_map);
		}

		replace_map["ARDUINO_SYSTEM_PATH"] = ToMsysSlash(system_path)
		replace_map["ARDUINO_VARIANT_PATH"] = ToMsysSlash(variant_path)
		if(recipe == "preproc.includes") {
			replace_map["ARDUINO_PREPROC_INCLUDES_FLAGS"]  = "\t" + strings.Join(includes, " ")
			replace_map["ARDUINO_PREPROC_INCLUDES_SOURCE"] = "\t" + ToMsysSlash(source)
			replace_map["ARDUINO_PREPROC_INCLUDES_OUTFILE"] = "\t" + ToMsysSlash(target)
		} else {
			replace_map["ARDUINO_PREPROC_MACROS_FLAGS"]    = "\t" + strings.Join(includes, " ")
			replace_map["ARDUINO_PREPROC_MACROS_SOURCE"]   = "\t" + ToMsysSlash(source)
			replace_map["ARDUINO_PREPROC_MACROS_OUTFILE"]   = "\t" + ToMsysSlash(target)
		}

		_, err = encode_to_file(preprocfile, replace_map)

		out := format_makefile(template, replace_map)

		os.Remove(build_path + string(os.PathSeparator) + "Makefile")
		write_file(build_path + string(os.PathSeparator) + "Makefile", []byte(out))

		exitStatus := ExecCommand("make", args...)
		os.Exit(exitStatus)

	} else if recipe == "makefile" {
		genmfs, _ := filepath.Glob(build_path + string(os.PathSeparator) + "*.genmf")
		commands := make([]Command, 0)
		for _, f := range genmfs {
			cmd := Command{}
			decode_from_file(f, &cmd);
			commands = append(commands, cmd)
		}

		cores_srcs := func() string {
			cores_srcs := select_command(commands, func (c Command) bool {
				return (strings.HasSuffix(c.Recipe, ".o") && c.Stage == "core" && strings.HasPrefix(c.Source, core_path) )
			} )

			cores_list := collect_string(cores_srcs, func (c Command) string { return ToMsysSlash(c.Source) } )
			return ("\t" + strings.Join(cores_list, " \\\n\t") + "\n")
		}

		variant_srcs := func() string {
			var_srcs := select_command(commands, func (c Command) bool {
				return (strings.HasSuffix(c.Recipe, ".o") && c.Stage == "core" && strings.HasPrefix(c.Source, variant_path) )
			} )

			var_list := collect_string(var_srcs, func (c Command) string { return ToMsysSlash(c.Source) } )
			return ("\t" + strings.Join(var_list, " \\\n\t") + "\n")
		}

		libs_srcs := func() string {
			libcmds := select_command(commands, func (c Command) bool {
				return (strings.HasSuffix(c.Recipe, ".o") && c.Stage == "libraries")
			})

			libs_srcs := collect_string(libcmds, func (c Command) string { return ToMsysSlash(c.Source) } )
			return ("\t" + strings.Join(libs_srcs, " \\\n\t") + "\n")
		}

		sketch_flags := func() string {
			flgs:= []string{}

			libcmds := select_command(commands, func (c Command) bool {
				return (strings.HasSuffix(c.Recipe, ".o") && c.Stage == "sketch")
			})


			for _, cmd := range libcmds {
				for _, flg := range cmd.Flags {
					if !contains(flgs, flg) {
						if (strings.HasPrefix(flg, "-I") || strings.HasPrefix(flg, "-L") ) {
							flg = flg[0:2] + ToMsysSlash(flg[2:])
						}
						flgs = append(flgs, flg)
					}
				}
			}

			return strings.Join(flgs, " ")
		}

		ldcmd := select_command(commands, func (c Command) bool {
			return (c.Recipe == "ld")
		})[0]

		replace_map := map[string]string {}

		preprocfile := build_path + string(os.PathSeparator) + "genmf.preproc"
		_, err := os.Stat(preprocfile)
		if !os.IsNotExist(err) {
			decode_from_file(preprocfile, &replace_map);
		}

		replace_map["ARDUINO_CFLAGS"] = sketch_flags()
		replace_map["ARDUINO_PROJECT_NAME"] = ToMsysSlash(ldcmd.ProjectName)
		replace_map["ARDUINO_SYSTEM_PATH"] = ToMsysSlash(ldcmd.SystemPath)
		replace_map["ARDUINO_BUILD_PATH"] = ToMsysSlash(ldcmd.BuildPath)
		replace_map["ARDUINO_CORE_PATH"] = ToMsysSlash(ldcmd.CorePath)
		replace_map["ARDUINO_VARIANT_PATH"] = ToMsysSlash(ldcmd.VariantPath)
		replace_map["ARDUINO_ARCHIVE_FILE"] = ToMsysSlash(ldcmd.ArchiveFile)
		replace_map["ARDUINO_CORES_SRCS"] = cores_srcs()
		replace_map["ARDUINO_VARIANT_SRCS"] = variant_srcs()
		replace_map["ARDUINO_LIBRARIES_SRCS"] = libs_srcs()
		replace_map["ARDUINO_VARIANT"] = variant_name
		replace_map["ARDUINO_PLATFORM_VERSION"] = platform_version

		out := format_makefile(template, replace_map)

		os.Remove(build_path + string(os.PathSeparator) + "Makefile")
		write_file(build_path + string(os.PathSeparator) + "Makefile", []byte(out))

		if verbose < 10  {
			for _, f := range genmfs {
				os.Remove(f)
			}
			os.Remove(build_path + string(os.PathSeparator) + "genmf.stage")
			os.Remove(build_path + string(os.PathSeparator) + "genmf.preproc")
		}
	}
}
