package gomobile_rules

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	accPublic  = 0x0001
	accPrivate = 0x0002
	accFinal   = 0x0010
)

type testMember struct {
	access           uint16
	name, descriptor string
}

type testClass struct {
	name       string
	access     uint16
	super      string
	interfaces []string
	fields     []testMember
	methods    []testMember
}

// classFile encodes a minimal class file: constant pool, hierarchy and
// member signatures.
func classFile(c testClass) []byte {
	var pool bytes.Buffer
	next := 1
	indexes := make(map[string]int)
	add := func(key string, entry []byte, width int) int {
		if index, ok := indexes[key]; ok {
			return index
		}
		pool.Write(entry)
		indexes[key] = next
		next += width
		return indexes[key]
	}
	utf8 := func(value string) int {
		entry := []byte{1, byte(len(value) >> 8), byte(len(value))}
		return add("u"+value, append(entry, value...), 1)
	}
	klass := func(value string) int {
		ref := utf8(value)
		return add("c"+value, []byte{7, byte(ref >> 8), byte(ref)}, 1)
	}
	u2 := func(buffer *bytes.Buffer, value int) {
		_ = binary.Write(buffer, binary.BigEndian, uint16(value))
	}
	this := klass(c.name)
	super := 0
	if c.super != "" {
		super = klass(c.super)
	}
	var interfaces []int
	for _, name := range c.interfaces {
		interfaces = append(interfaces, klass(name))
	}
	add("long", []byte{5, 0, 0, 0, 0, 0, 0, 0, 42}, 2)
	type ref struct{ access, name, descriptor int }
	encode := func(members []testMember) []ref {
		var refs []ref
		for _, item := range members {
			refs = append(refs, ref{int(item.access), utf8(item.name), utf8(item.descriptor)})
		}
		return refs
	}
	fields, methods := encode(c.fields), encode(c.methods)
	var output bytes.Buffer
	_ = binary.Write(&output, binary.BigEndian, uint32(0xCAFEBABE))
	u2(&output, 0)
	u2(&output, 52)
	u2(&output, next)
	output.Write(pool.Bytes())
	u2(&output, int(c.access))
	u2(&output, this)
	u2(&output, super)
	u2(&output, len(interfaces))
	for _, index := range interfaces {
		u2(&output, index)
	}
	for _, group := range [][]ref{fields, methods} {
		u2(&output, len(group))
		for _, item := range group {
			u2(&output, item.access)
			u2(&output, item.name)
			u2(&output, item.descriptor)
			u2(&output, 0)
		}
	}
	u2(&output, 0)
	return output.Bytes()
}

func bindingClasses() map[string]testClass {
	object := "java/lang/Object"
	classes := []testClass{
		{name: "go/Seq", access: accPublic, super: object, fields: []testMember{{accPublic | accStatic | accFinal, "nullRef", "Lgo/Seq$Ref;"}}, methods: []testMember{
			{accPublic | accStatic, "getRef", "(I)Lgo/Seq$Ref;"},
			{accStatic, "decRef", "(I)V"},
			{accPublic | accStatic, "incRefnum", "(I)V"},
			{accPublic | accStatic, "incRef", "(Ljava/lang/Object;)I"},
			{accPublic | accStatic, "incGoObjectRef", "(Lgo/Seq$GoObject;)I"},
			{accPublic | accStatic, "touch", "()V"},
			{accPublic | accStatic, "setContext", "(Landroid/content/Context;)V"},
			{accPrivate | accStatic | accNative, "init", "()V"},
			{accStatic | accNative, "setContext", "(Ljava/lang/Object;)V"},
			{accPublic | accStatic | accNative, "incGoRef", "(ILgo/Seq$GoObject;)V"},
			{accStatic | accNative, "destroyRef", "(I)V"},
		}},
		{name: "go/Seq$Ref", access: accPublic, super: object, fields: []testMember{{accPublic | accFinal, "refnum", "I"}, {accPublic | accFinal, "obj", "Ljava/lang/Object;"}}, methods: []testMember{{0, "<init>", "(ILjava/lang/Object;)V"}}},
		{name: "go/Seq$GoObject", access: accPublic | accInterface | accAbstract, super: object, methods: []testMember{{accPublic | accAbstract, "incRefnum", "()I"}}},
		{name: "go/Seq$Proxy", access: accPublic | accInterface | accAbstract, super: object, interfaces: []string{"go/Seq$GoObject"}},
		{name: "go/Universe", access: accPublic | accAbstract, super: object, methods: []testMember{{accPublic | accStatic, "touch", "()V"}, {accPrivate | accStatic | accNative, "_init", "()V"}}},
		{name: "go/Universe$proxyerror", super: "java/lang/Exception", interfaces: []string{"go/Seq$Proxy", "go/error"}, methods: []testMember{{0, "<init>", "(I)V"}, {accPublic | accNative, "error", "()Ljava/lang/String;"}}},
		{name: "go/error", access: accPublic | accInterface | accAbstract, super: object, methods: []testMember{{accPublic | accAbstract, "error", "()Ljava/lang/String;"}}},
		{name: "demo/Demo", access: accPublic | accAbstract, super: object, methods: []testMember{{accPrivate | accStatic | accNative, "_init", "()V"}, {accPublic | accStatic | accNative, "newPoint", "(Ldemo/Callback;Z)Ldemo/Point;"}}},
		{name: "demo/Demo$proxyCallback", access: accFinal, super: object, interfaces: []string{"go/Seq$Proxy", "demo/Callback"}, methods: []testMember{{0, "<init>", "(I)V"}, {accPublic | accNative, "protect", "(J)Z"}, {accPublic | accNative, "onStatus", "(JLjava/lang/String;)J"}}},
		{name: "demo/Callback", access: accPublic | accInterface | accAbstract, super: object, methods: []testMember{{accPublic | accAbstract, "protect", "(J)Z"}, {accPublic | accAbstract, "onStatus", "(JLjava/lang/String;)J"}}},
		{name: "demo/Point", access: accPublic | accFinal, super: object, interfaces: []string{"go/Seq$Proxy"}, fields: []testMember{{accPrivate | accFinal, "refnum", "I"}}, methods: []testMember{
			{0, "<init>", "(I)V"},
			{accPublic, "<init>", "(Ldemo/Callback;Z)V"},
			{accPrivate | accStatic | accNative, "__NewPoint", "(Ldemo/Callback;Z)I"},
			{accPublic | accNative, "runLoop", "(Z)V"},
			{accPublic, "hashCode", "()I"},
		}},
	}
	result := make(map[string]testClass)
	for _, item := range classes {
		result[item.name] = item
	}
	return result
}

func jar(t *testing.T, classes map[string]testClass) []byte {
	t.Helper()
	names := make([]string, 0, len(classes))
	for name := range classes {
		names = append(names, name)
	}
	sort.Strings(names)
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, name := range names {
		entry, err := writer.Create(name + ".class")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write(classFile(classes[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// expectedDemoRules is shared verbatim with the reference implementation in
// VPNProtocols (scripts/release/tests/test_gomobile_jni_contract.py).
const expectedDemoRules = header + `-keep,includedescriptorclasses class go.Seq {
    void decRef(int);
    go.Seq$Ref getRef(int);
    int incGoObjectRef(go.Seq$GoObject);
    int incRef(java.lang.Object);
    void incRefnum(int);
}
-keep class go.Seq$Ref {
    java.lang.Object obj;
}
-keep,includedescriptorclasses interface demo.Callback {
    <methods>;
}
-keep class demo.Demo$proxyCallback {
    <init>(int);
}
-keep class demo.Point {
    <init>(int);
}
-keep class go.Universe$proxyerror {
    <init>(int);
}
-keepclasseswithmembernames,includedescriptorclasses class demo.Demo {
    native <methods>;
}
-keepclasseswithmembernames,includedescriptorclasses class demo.Demo$proxyCallback {
    native <methods>;
}
-keepclasseswithmembernames,includedescriptorclasses class demo.Point {
    native <methods>;
}
-keepclasseswithmembernames,includedescriptorclasses class go.Seq {
    native <methods>;
}
-keepclasseswithmembernames,includedescriptorclasses class go.Universe {
    native <methods>;
}
-keepclasseswithmembernames,includedescriptorclasses class go.Universe$proxyerror {
    native <methods>;
}
`

func TestGenerateNamesEveryLookupExactly(t *testing.T) {
	t.Parallel()
	rules, err := Generate(jar(t, bindingClasses()))
	if err != nil {
		t.Fatal(err)
	}
	if rules != expectedDemoRules {
		t.Fatalf("unexpected rules:\n%s", rules)
	}
	for _, line := range strings.Split(rules, "\n") {
		if strings.Contains(line, "*") {
			t.Fatalf("wildcard in %q", line)
		}
	}
}

func TestHostClassesAreNotRoots(t *testing.T) {
	t.Parallel()
	classes := bindingClasses()
	classes["demo/HostCallback"] = testClass{name: "demo/HostCallback", access: accPublic, super: "java/lang/Object", interfaces: []string{"demo/Callback"}}
	rules, err := Generate(jar(t, classes))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rules, "HostCallback") {
		t.Fatalf("host class kept:\n%s", rules)
	}
}

func TestGeneratorInvariantsFailClosed(t *testing.T) {
	t.Parallel()
	cases := map[string]func(map[string]testClass){
		"refnum constructor": func(c map[string]testClass) {
			c["demo/Point"] = testClass{name: "demo/Point", super: "java/lang/Object", interfaces: []string{"go/Seq$Proxy"}}
		},
		"interface proxy": func(c map[string]testClass) { delete(c, "demo/Demo$proxyCallback") },
		"overloaded native": func(c map[string]testClass) {
			c["demo/Point"] = testClass{name: "demo/Point", super: "java/lang/Object", interfaces: []string{"go/Seq$Proxy"}, methods: []testMember{{0, "<init>", "(I)V"}, {accNative, "run", "()V"}, {accNative, "run", "(Z)V"}}}
		},
		"Seq method": func(c map[string]testClass) {
			c["go/Seq"] = testClass{name: "go/Seq", super: "java/lang/Object", methods: []testMember{{accStatic, "getRef", "(I)Lgo/Seq$Ref;"}}}
		},
		"Seq.Ref field": func(c map[string]testClass) {
			c["go/Seq$Ref"] = testClass{name: "go/Seq$Ref", super: "java/lang/Object"}
		},
	}
	for label, mutate := range cases {
		classes := bindingClasses()
		mutate(classes)
		if _, err := Generate(jar(t, classes)); err == nil {
			t.Errorf("%s: expected an error", label)
		}
	}
}

func TestReplaceInAARKeepsEveryOtherEntryByteForByte(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "libdemo.aar")
	var original bytes.Buffer
	writer := zip.NewWriter(&original)
	entries := []struct{ name, content string }{
		{"AndroidManifest.xml", `<manifest package="go.demo.gojni"/>`},
		{"proguard.txt", "-keep class go.** { *; }\n-keep class demo.** { *; }\n"},
		{"classes.jar", string(jar(t, bindingClasses()))},
		{"jni/arm64-v8a/libdemo.so", strings.Repeat("\x7fELF native payload ", 64)},
		{"R.txt", ""},
	}
	for _, item := range entries {
		entry, err := writer.Create(item.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(entry, item.content); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.CreateHeader(&zip.FileHeader{Name: "res/"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceInAAR(path); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := zip.NewReader(bytes.NewReader(original.Bytes()), int64(original.Len()))
	after, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if len(after.File) != len(before.File) {
		t.Fatalf("entry count changed: %d -> %d", len(before.File), len(after.File))
	}
	for i, file := range after.File {
		if file.Name != before.File[i].Name {
			t.Fatalf("entry order changed at %d: %s", i, file.Name)
		}
		payload, err := readEntry(file)
		if err != nil {
			t.Fatal(err)
		}
		if file.Name == "proguard.txt" {
			if string(payload) != expectedDemoRules {
				t.Fatalf("unexpected proguard.txt:\n%s", payload)
			}
			continue
		}
		raw, err := file.OpenRaw()
		if err != nil {
			t.Fatal(err)
		}
		rawAfter, _ := io.ReadAll(raw)
		raw, _ = before.File[i].OpenRaw()
		rawBefore, _ := io.ReadAll(raw)
		if !bytes.Equal(rawAfter, rawBefore) || file.CRC32 != before.File[i].CRC32 || file.Method != before.File[i].Method || file.Flags != before.File[i].Flags {
			t.Fatalf("entry %s changed", file.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, ".libdemo.aar.rules")); !os.IsNotExist(err) {
		t.Fatalf("temporary file left behind: %v", err)
	}
}

func TestReplaceInAARRequiresTheGomobileEntries(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "libdemo.aar")
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, _ := writer.Create("classes.jar")
	_, _ = entry.Write(jar(t, bindingClasses()))
	_ = writer.Close()
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceInAAR(path); err == nil {
		t.Fatal("expected an error for an AAR without proguard.txt")
	}
}
