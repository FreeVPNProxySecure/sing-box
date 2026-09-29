// Package gomobile_rules derives the consumer keep rules of a gomobile Android
// binding from the classes the generator produced.
//
// gomobile writes `-keep class go.** { *; }` and `-keep class <javapkg>.** { *; }`
// into every AAR. Those rules keep every member of the binding and any class of
// the consuming application under the same package prefix. The native code of a
// binding only needs the classes and members it resolves by name:
//
//   - bind/java/seq_android.c.support, Java_go_Seq_init: the static methods
//     go.Seq.getRef, decRef, incRefnum, incRef and incGoObjectRef, the class
//     go.Seq$Ref and its field obj. A failed lookup is fatal.
//   - bind/genjava.go, GenC (<Package>._init of every bound package and of the
//     universe package): FindClass of every class that proxies a Go object with
//     GetMethodID("<init>", "(I)V"), and FindClass of every generated interface
//     with GetMethodID of each of its methods. Go calls Java implementations of
//     these interfaces through those method IDs.
//   - Java calls into Go through statically registered native methods
//     (Java_<class>_<method> exports). The generator has no RegisterNatives or
//     JNI_OnLoad.
//
// Reverse bindings (bind/genclasses.go) add further lookups; a binding that
// uses them is rejected.
package gomobile_rules

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const header = "# Generated from the gomobile JNI contract of this binding: native code\n" +
	"# resolves these classes and members by name. Do not edit by hand.\n"

const (
	accStatic    = 0x0008
	accNative    = 0x0100
	accInterface = 0x0200
	accAbstract  = 0x0400

	proxyInterface = "go/Seq$Proxy"
	seqClass       = "go/Seq"
	seqRefClass    = "go/Seq$Ref"
	runtimePackage = "go"
)

// seqStaticMethods are resolved with GetStaticMethodID in Java_go_Seq_init.
var seqStaticMethods = [][2]string{
	{"getRef", "(I)Lgo/Seq$Ref;"},
	{"decRef", "(I)V"},
	{"incRefnum", "(I)V"},
	{"incRef", "(Ljava/lang/Object;)I"},
	{"incGoObjectRef", "(Lgo/Seq$GoObject;)I"},
}

type member struct {
	access     uint16
	name       string
	descriptor string
}

type class struct {
	name       string
	access     uint16
	interfaces []string
	fields     []member
	methods    []member
}

func (c *class) pkg() string {
	if i := strings.LastIndexByte(c.name, '/'); i >= 0 {
		return c.name[:i]
	}
	return ""
}

func (c *class) find(members []member, name, descriptor string) *member {
	for i := range members {
		if members[i].name == name && members[i].descriptor == descriptor {
			return &members[i]
		}
	}
	return nil
}

func (c *class) implements(name string) bool {
	for _, i := range c.interfaces {
		if i == name {
			return true
		}
	}
	return false
}

// Generate returns the keep rules for the classes of a gomobile classes.jar.
func Generate(classesJar []byte) (string, error) {
	classes, err := readClasses(classesJar)
	if err != nil {
		return "", err
	}
	return render(classes)
}

// ReplaceInAAR replaces the proguard.txt that gomobile wrote into the AAR at
// path with the rules generated from its classes.jar. All other entries are
// copied without recompression and in their original order.
func ReplaceInAAR(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return err
	}
	var classesJar []byte
	for _, file := range reader.File {
		if file.Name == "classes.jar" {
			if classesJar, err = readEntry(file); err != nil {
				return err
			}
		}
	}
	if classesJar == nil {
		return fmt.Errorf("%s has no classes.jar", path)
	}
	rules, err := Generate(classesJar)
	if err != nil {
		return err
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	replaced := false
	for _, file := range reader.File {
		if file.Name != "proguard.txt" {
			if err = writer.Copy(file); err != nil {
				return err
			}
			continue
		}
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: file.Name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		if _, err = io.WriteString(entry, rules); err != nil {
			return err
		}
		replaced = true
	}
	if !replaced {
		return fmt.Errorf("%s has no proguard.txt", path)
	}
	if err = writer.Close(); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".rules")
	if err = os.WriteFile(temporary, output.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func readEntry(file *zip.File) ([]byte, error) {
	entry, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer entry.Close()
	return io.ReadAll(entry)
}

func readClasses(classesJar []byte) (map[string]*class, error) {
	reader, err := zip.NewReader(bytes.NewReader(classesJar), int64(len(classesJar)))
	if err != nil {
		return nil, err
	}
	classes := make(map[string]*class)
	for _, file := range reader.File {
		if !strings.HasSuffix(file.Name, ".class") {
			continue
		}
		content, err := readEntry(file)
		if err != nil {
			return nil, err
		}
		parsed, err := parseClass(content)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file.Name, err)
		}
		if parsed.name+".class" != file.Name {
			return nil, fmt.Errorf("%s declares %s", file.Name, parsed.name)
		}
		classes[parsed.name] = parsed
	}
	return classes, nil
}

type classReader struct {
	data []byte
	pos  int
	err  error
}

func (r *classReader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if r.pos+n > len(r.data) {
		r.err = fmt.Errorf("truncated class file")
		return nil
	}
	value := r.data[r.pos : r.pos+n]
	r.pos += n
	return value
}

func (r *classReader) u1() uint8 {
	if value := r.take(1); value != nil {
		return value[0]
	}
	return 0
}

func (r *classReader) u2() uint16 {
	if value := r.take(2); value != nil {
		return binary.BigEndian.Uint16(value)
	}
	return 0
}

func (r *classReader) u4() uint32 {
	if value := r.take(4); value != nil {
		return binary.BigEndian.Uint32(value)
	}
	return 0
}

var constantSizes = map[uint8]int{3: 4, 4: 4, 5: 8, 6: 8, 7: 2, 8: 2, 9: 4, 10: 4, 11: 4, 12: 4, 15: 3, 16: 2, 17: 4, 18: 4, 19: 2, 20: 2}

func parseClass(data []byte) (*class, error) {
	r := &classReader{data: data}
	if r.u4() != 0xCAFEBABE {
		return nil, fmt.Errorf("not a class file")
	}
	r.u2()
	r.u2()
	count := int(r.u2())
	utf8 := make(map[int]string)
	classRefs := make(map[int]int)
	for index := 1; index < count && r.err == nil; index++ {
		tag := r.u1()
		switch {
		case tag == 1:
			utf8[index] = string(r.take(int(r.u2())))
		case tag == 7:
			classRefs[index] = int(r.u2())
		case constantSizes[tag] > 0:
			r.take(constantSizes[tag])
			if tag == 5 || tag == 6 {
				index++
			}
		default:
			return nil, fmt.Errorf("unknown constant pool tag %d", tag)
		}
	}
	className := func(ref uint16) string {
		return utf8[classRefs[int(ref)]]
	}
	parsed := &class{access: r.u2()}
	parsed.name = className(r.u2())
	r.u2()
	for i := r.u2(); i > 0; i-- {
		parsed.interfaces = append(parsed.interfaces, className(r.u2()))
	}
	members := func() []member {
		var result []member
		for i := r.u2(); i > 0 && r.err == nil; i-- {
			item := member{access: r.u2(), name: utf8[int(r.u2())], descriptor: utf8[int(r.u2())]}
			for j := r.u2(); j > 0 && r.err == nil; j-- {
				r.u2()
				r.take(int(r.u4()))
			}
			result = append(result, item)
		}
		return result
	}
	parsed.fields = members()
	parsed.methods = members()
	if r.err != nil {
		return nil, r.err
	}
	if parsed.name == "" {
		return nil, fmt.Errorf("class file without a name")
	}
	return parsed, nil
}

func packageClass(pkg string) string {
	if pkg == runtimePackage {
		return runtimePackage + "/Universe"
	}
	last := pkg[strings.LastIndexByte(pkg, '/')+1:]
	return pkg + "/" + strings.ToUpper(last[:1]) + last[1:]
}

func sortedKeys(classes map[string]*class) []string {
	names := make([]string, 0, len(classes))
	for name := range classes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func javaName(binaryName string) string {
	return strings.ReplaceAll(binaryName, "/", ".")
}

func javaType(descriptor string) (string, int) {
	dimensions := 0
	for descriptor[dimensions] == '[' {
		dimensions++
	}
	var name string
	consumed := dimensions + 1
	switch descriptor[dimensions] {
	case 'L':
		end := strings.IndexByte(descriptor, ';')
		name = javaName(descriptor[dimensions+1 : end])
		consumed = end + 1
	case 'B':
		name = "byte"
	case 'C':
		name = "char"
	case 'D':
		name = "double"
	case 'F':
		name = "float"
	case 'I':
		name = "int"
	case 'J':
		name = "long"
	case 'S':
		name = "short"
	case 'Z':
		name = "boolean"
	case 'V':
		name = "void"
	}
	return name + strings.Repeat("[]", dimensions), consumed
}

func javaSignature(name, descriptor string) string {
	var parameters []string
	position := 1
	for descriptor[position] != ')' {
		value, consumed := javaType(descriptor[position:])
		parameters = append(parameters, value)
		position += consumed
	}
	result, _ := javaType(descriptor[position+1:])
	return result + " " + name + "(" + strings.Join(parameters, ",") + ")"
}

func rule(option, kind, name string, members ...string) string {
	var builder strings.Builder
	builder.WriteString(option + " " + kind + " " + javaName(name) + " {\n")
	for _, item := range members {
		builder.WriteString("    " + item + ";\n")
	}
	builder.WriteString("}\n")
	return builder.String()
}

func render(classes map[string]*class) (string, error) {
	seq, ref := classes[seqClass], classes[seqRefClass]
	if seq == nil || ref == nil {
		return "", fmt.Errorf("gomobile runtime go.Seq or go.Seq$Ref is missing")
	}
	var proxies, interfaces, natives []string
	boundPackages := make(map[string]bool)
	for _, name := range sortedKeys(classes) {
		if item := classes[name]; item.implements(proxyInterface) {
			proxies = append(proxies, name)
			if item.pkg() != runtimePackage {
				boundPackages[item.pkg()] = true
			}
		}
	}
	if len(boundPackages) == 0 {
		return "", fmt.Errorf("no bound package: no class implements go.Seq$Proxy outside go")
	}
	for _, name := range sortedKeys(classes) {
		if item := classes[name]; item.access&accInterface != 0 && boundPackages[item.pkg()] {
			interfaces = append(interfaces, name)
		}
	}

	var seqMembers []string
	sortedSeq := append([][2]string(nil), seqStaticMethods...)
	sort.Slice(sortedSeq, func(i, j int) bool {
		if sortedSeq[i][0] != sortedSeq[j][0] {
			return sortedSeq[i][0] < sortedSeq[j][0]
		}
		return sortedSeq[i][1] < sortedSeq[j][1]
	})
	for _, method := range sortedSeq {
		found := seq.find(seq.methods, method[0], method[1])
		if found == nil || found.access&accStatic == 0 {
			return "", fmt.Errorf("go.Seq lacks static %s%s", method[0], method[1])
		}
		seqMembers = append(seqMembers, javaSignature(method[0], method[1]))
	}
	if ref.find(ref.fields, "obj", "Ljava/lang/Object;") == nil {
		return "", fmt.Errorf("go.Seq$Ref lacks field obj")
	}

	type lookup struct{ name, rule string }
	var lookups []lookup
	for _, name := range proxies {
		item := classes[name]
		if item.pkg() != runtimePackage && !boundPackages[item.pkg()] {
			return "", fmt.Errorf("proxy class outside the bound packages: %s", name)
		}
		if item.find(item.methods, "<init>", "(I)V") == nil {
			return "", fmt.Errorf("proxy class lacks the refnum constructor: %s", name)
		}
		simple := name[strings.LastIndexByte(name, '/')+1:]
		if owner, proxied, ok := strings.Cut(simple, "$proxy"); ok {
			if item.pkg()+"/"+owner != packageClass(item.pkg()) {
				return "", fmt.Errorf("interface proxy outside its package class: %s", name)
			}
			proxiedInterface := classes[item.pkg()+"/"+proxied]
			if item.pkg() != runtimePackage && (proxiedInterface == nil || proxiedInterface.access&accInterface == 0) {
				return "", fmt.Errorf("interface proxy without its interface: %s", name)
			}
		}
		lookups = append(lookups, lookup{name, rule("-keep", "class", name, "<init>(int)")})
	}
	for _, name := range interfaces {
		item := classes[name]
		simple := name[strings.LastIndexByte(name, '/')+1:]
		if classes[packageClass(item.pkg())+"$proxy"+simple] == nil {
			return "", fmt.Errorf("interface without its proxy class: %s", name)
		}
		methods := 0
		for _, method := range item.methods {
			if method.name == "<clinit>" {
				continue
			}
			if method.access&accAbstract == 0 || method.access&accStatic != 0 {
				return "", fmt.Errorf("generated interface has unexpected methods: %s", name)
			}
			methods++
		}
		if methods == 0 {
			return "", fmt.Errorf("generated interface has unexpected methods: %s", name)
		}
		// Every declared method is resolved with GetMethodID: the generator
		// declares exactly the methods whose signatures it supports.
		lookups = append(lookups, lookup{name, rule("-keep,includedescriptorclasses", "interface", name, "<methods>")})
	}
	sort.Slice(lookups, func(i, j int) bool { return lookups[i].name < lookups[j].name })

	for _, name := range sortedKeys(classes) {
		item := classes[name]
		seen := make(map[string]bool)
		for _, method := range item.methods {
			if method.access&accNative == 0 {
				continue
			}
			if item.pkg() != runtimePackage && !boundPackages[item.pkg()] {
				return "", fmt.Errorf("native method outside the bound packages: %s", name)
			}
			if seen[method.name] {
				return "", fmt.Errorf("overloaded native method needs long JNI names: %s.%s", name, method.name)
			}
			seen[method.name] = true
		}
		if len(seen) > 0 {
			natives = append(natives, name)
		}
	}

	var builder strings.Builder
	builder.WriteString(header)
	builder.WriteString(rule("-keep,includedescriptorclasses", "class", seqClass, seqMembers...))
	builder.WriteString(rule("-keep", "class", seqRefClass, "java.lang.Object obj"))
	for _, item := range lookups {
		builder.WriteString(item.rule)
	}
	for _, name := range natives {
		builder.WriteString(rule("-keepclasseswithmembernames,includedescriptorclasses", "class", name, "native <methods>"))
	}
	return builder.String(), nil
}
