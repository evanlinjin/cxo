package skyobject

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/skycoin/skycoin/src/cipher"
	"github.com/skycoin/skycoin/src/cipher/encoder"
)

// TAG is name of struct tag the skyobject package use to determine
// schema of a struct field if the field is reference
const TAG = "skyobject"

// ErrInvalidEncodedSchema occurs during decoding an invalid registry
var ErrInvalidEncodedSchema = errors.New("invalid encoded schema")

// A Reg used to create new Registry
type Reg struct {
	tn map[reflect.Type]string // type -> registered name
}

func newReg() *Reg {
	return &Reg{
		tn: make(map[reflect.Type]string),
	}
}

// Register type of given value with given name. If
// given value is pointer, then it will be converted to
// non-pointer inside. E.g. it registers non-pointer types
// only
func (r *Reg) Register(name string, val interface{}) {
	if name == "" {
		panic("empty name")
	}
	typ := typeOf(val)
	switch typ {
	case typeOfRef, typeOfRefs, typeOfDynamic:
		panic("can't register reference type")
	default:
	}

	for _, n := range r.tn {
		if n == name {
			panic("this name already registered: " + name)
		}
	}

	r.tn[typ] = name
}

// use (reflect.Type).Name() or name provided to Register;
// if there aren't, then return nil
func (r *Reg) typeName(typ reflect.Type) []byte {
	if name, ok := r.tn[typ]; ok {
		return []byte(name)
	}
	if name := typ.Name(); name != "" {
		return []byte(name)
	}
	return nil
}

func (r *Reg) getSchema(typ reflect.Type) Schema {

	if typ == typeOfDynamic { // dynamic reference
		return &referenceSchema{
			schema: schema{
				ref:  SchemaRef{},
				kind: typ.Kind(),
			},
			typ: ReferenceTypeDynamic,
		}
	}

	if typ == typeOfRef || typ == typeOfRefs {
		panic("Ref or Refs are not allowed in arrays and slices")
	}

	switch typ.Kind() {

	case reflect.Bool, reflect.Int8, reflect.Uint8,
		reflect.Int16, reflect.Uint16,
		reflect.Int32, reflect.Uint32, reflect.Float32,
		reflect.Int64, reflect.Uint64, reflect.Float64,
		reflect.String:

		s := new(schema)
		s.kind, s.name = typ.Kind(), r.typeName(typ)
		return s

	case reflect.Slice:

		// get schema of element

		ss := new(sliceSchema)
		ss.kind, ss.name = typ.Kind(), r.typeName(typ)

		el := r.getSchema(typ.Elem())

		if el.IsRegistered() {
			ss.elem = &schema{SchemaRef{}, el.Kind(), el.RawName()}
			return ss
		}

		ss.elem = el
		return ss

	case reflect.Array:

		// get schema of element and length

		as := new(arraySchema)
		as.kind, as.name = typ.Kind(), r.typeName(typ)
		as.length = typ.Len()

		el := r.getSchema(typ.Elem())

		if el.IsRegistered() {
			as.elem = &schema{SchemaRef{}, el.Kind(), el.RawName()}
			return as
		}

		as.elem = el
		return as

	case reflect.Struct:

		// get schemas of fields

		ss := new(structSchema)
		ss.kind, ss.name = typ.Kind(), r.typeName(typ)

		for i, nf := 0, typ.NumField(); i < nf; i++ {

			sf := typ.Field(i)
			if sf.Tag.Get("enc") == "-" || sf.PkgPath != "" || sf.Name == "_" {
				continue
			}
			ss.fields = append(ss.fields, r.getField(sf))

		}

		return ss

	default:
	}

	panic("invlaid type: " + typ.String())

}

func (r *Reg) getField(sf reflect.StructField) Field {

	f := new(field)

	f.name = []byte(sf.Name)
	f.tag = []byte(sf.Tag)

	t := sf.Type // reflect.Type

	switch t {
	case typeOfRef: // reference
		tagRef := mustTagSchemaName(sf.Tag)
		f.schema = &referenceSchema{
			schema: schema{
				ref:  SchemaRef{},
				kind: t.Kind(),
			},
			typ:  ReferenceTypeSingle,
			elem: &schema{kind: reflect.Struct, name: []byte(tagRef)},
		}
		return f
	case typeOfRefs: // references
		tagRef := mustTagSchemaName(sf.Tag)
		f.schema = &referenceSchema{
			schema: schema{
				ref:  SchemaRef{},
				kind: t.Kind(),
			},
			typ:  ReferenceTypeSlice,
			elem: &schema{kind: reflect.Struct, name: []byte(tagRef)},
		}
		return f
	case typeOfDynamic: // dynamic reference
		f.schema = &referenceSchema{
			schema: schema{
				ref:  SchemaRef{},
				kind: t.Kind(),
			},
			typ: ReferenceTypeDynamic,
		}
		return f
	default:
	}

	if s := r.getSchema(sf.Type); s.IsRegistered() {
		f.schema = &schema{SchemaRef{}, s.Kind(), s.RawName()}
	} else {
		f.schema = s
	}

	return f

}

// A Registry represents types registry
type Registry struct {
	done bool // stop registration and use

	ref RegistryRef // reference to the registry

	reg map[string]Schema    // by name
	srf map[SchemaRef]Schema // by reference (for Dynamic references)

	// local (inversed tn of Reg for unpacking directly to reflect.Type)
	nt map[string]reflect.Type // registered name -> reflect.Type
	tn map[reflect.Type]string // reflect.Type -> regitered name
}

// create registry without nt map
func newRegistry() (r *Registry) {
	r = new(Registry)
	r.reg = make(map[string]Schema)
	r.srf = make(map[SchemaRef]Schema)
	return
}

// DecodeRegistry decodes registry. It's impossible to
// use SchemaByInterface of an decoded Registry. A decoded
// Registry already Done
func DecodeRegistry(b []byte) (r *Registry, err error) {
	var (
		res = registryEntities{}
		s   Schema
	)
	if err = encoder.DeserializeRaw(b, &res); err != nil {
		return
	}
	r = newRegistry()
	for _, re := range res {
		s, err = decodeSchema(re.Schema)
		r.reg[re.Name] = s
		r.srf[s.Reference()] = s
	}
	r.finialize()
	return
}

// NewRegistry creates filled up Registry using provided
// function. For example
//
//     reg := skyobject.NewRegistry(func(t *skyobject.Reg) {
//         t.Register("cxo.User", User{})
//         t.Register("cxo.Group", Group{})
//         t.Register("cxo.Any", Any{})
//     })
//
func NewRegistry(cl func(t *Reg)) (r *Registry) {
	reg := newReg()
	cl(reg)

	r = newRegistry()
	r.nt = make(map[string]reflect.Type)

	r.register(reg)
	r.finialize()

	return
}

// Encode registry to send
func (r *Registry) Encode() []byte {
	if len(r.reg) == 0 {
		return encoder.Serialize(registryEntities{}) // empty
	}
	ent := make(registryEntities, 0, len(r.reg))
	for name, sch := range r.reg {
		ent = append(ent, registryEntity{name, sch.Encode()})
	}
	sort.Sort(ent)
	return encoder.Serialize(ent)
}

// Reference of the Registry
func (r *Registry) Reference() RegistryRef {
	return r.ref
}

// SchemaByReference returns Schema by SchemaRef that is obvious.
func (r *Registry) SchemaByReference(sr SchemaRef) (s Schema, err error) {
	var ok bool
	if s, ok = r.srf[sr]; !ok {
		err = fmt.Errorf("missng schema %q", sr.String())
	}
	return
}

// SchemaByName returns schema by name or "missing schema" error
func (r *Registry) SchemaByName(name string) (Schema, error) {
	return r.schemaByName(name)
}

// Types returns Types of the Registry. If this registry created using
// DecodeRegistry (received from network) then result will not
// be valid (empty maps). The Types used to pack/unpack CX objects
// directly from and to golang values. You should not modify the
// maps of the Types
func (r *Registry) Types() (ts *Types) {
	ts = new(Types)
	ts.Direct = r.nt
	ts.Inverse = r.tn
	return
}

// range over registered types, and create schemas
func (r *Registry) register(reg *Reg) {

	r.tn = reg.tn // keep the map

	for typ, name := range reg.tn {
		r.nt[name] = typ // build r.nt by the reg.tn
		s := reg.getSchema(typ)
		// only named structures
		if !s.IsRegistered() {
			panic("can't register type: " + typ.Name())
		}
		r.reg[name] = s // store: name -> Scehma
	}

}

// set proper references for schemas that has references to
// another schemas, such as arrays, slices and structs
func (r *Registry) fillSchema(s Schema, filled map[Schema]struct{}) {
	if _, ok := filled[s]; ok {
		return // already
	}
	filled[s] = struct{}{} // filling
	var err error
	if s.IsReference() {
		switch s.ReferenceType() {
		case ReferenceTypeSingle, ReferenceTypeSlice:
			x := s.(*referenceSchema)
			x.elem, err = r.schemaByName(x.elem.Name())
			if err != nil {
				panic(err)
			}
			r.fillSchema(x.elem, filled)
		case ReferenceTypeDynamic:
			// do nothing
		default:
			panic("invalid reference: " + s.String())
		}
		return
	}
	switch s.Kind() {
	case reflect.Array:
		x := s.(*arraySchema)
		if s.Elem().IsRegistered() {
			x.elem, err = r.schemaByName(s.Elem().Name())
			if err != nil {
				panic(err)
			}
		}
		r.fillSchema(x.elem, filled)
	case reflect.Slice:
		x := s.(*sliceSchema)
		if s.Elem().IsRegistered() {
			x.elem, err = r.schemaByName(s.Elem().Name())
			if err != nil {
				panic(err)
			}
		}
		r.fillSchema(x.elem, filled)
	case reflect.Struct:
		for i, f := range s.Fields() {
			x := f.(*field)
			if fs := f.Schema(); fs.IsRegistered() {
				x.schema, err = r.schemaByName(fs.Name())
				if err != nil {
					panic(err)
				}
			}
			r.fillSchema(x.schema, filled)
			s.(*structSchema).fields[i] = x
		}
	}
}

func (r *Registry) schemaByName(name string) (s Schema, err error) {
	var ok bool
	if s, ok = r.reg[name]; !ok {
		err = fmt.Errorf("missing schema %q", name)
	}
	return
}

func (r *Registry) finialize() {
	filled := make(map[Schema]struct{})
	for _, sch := range r.reg {
		r.fillSchema(sch, filled)
	}

	// fill up map by SchemaRef
	for _, sch := range r.reg {
		r.srf[sch.Reference()] = sch
	}

	encoded := r.Encode()
	r.ref = RegistryRef(cipher.SumSHA256(encoded))
}

// TagSchemaName returns schema name from given reflect.StructTag.
// E.g. it returns "User" if tag is `skyobject:"schema=User" json:"blah"`.
// It returns error if given tag doesn't contain the `skyobject:"schema=XXX"`
func TagSchemaName(tag reflect.StructTag) (sch string, err error) {
	skytag := tag.Get(TAG)
	if skytag == "" {
		err = errors.New(`empty skyobject tag, expected "schema=XXX"`)
		return
	}
	for _, part := range strings.Split(skytag, ",") {
		if !strings.HasPrefix(part, "schema=") {
			continue
		}
		ss := strings.Split(part, "=")
		if len(ss) != 2 {
			err = fmt.Errorf("invalid schema tag: %q", part)
			return
		}
		if ss[1] == "" {
			err = fmt.Errorf("empty tag schema name: %q", part)
			return
		}
		sch = ss[1]
		return
	}
	err = fmt.Errorf("invalid skyobject tag: %q", skytag)
	return
}

func mustTagSchemaName(tag reflect.StructTag) string {
	sch, err := TagSchemaName(tag)
	if err != nil {
		panic(err)
	}
	return sch
}

func typeOf(i interface{}) reflect.Type {
	return reflect.Indirect(reflect.ValueOf(i)).Type()
}

// decode schema

func decodeSchema(b []byte) (s Schema, err error) {
	// type encodedSchema struct {
	// 	ReferenceType uint32
	// 	Kind   uint32
	// 	Name   []byte
	// 	Len    uint32
	// 	Fields [][]byte
	// 	Elem   []byte // encoded schema
	// }
	//
	// type encodedField struct {
	// 	Name   []byte
	// 	Tag    []byte
	// 	Schema []byte
	// }

	var x encodedSchema
	if err = encoder.DeserializeRaw(b, &x); err != nil {
		return
	}
	// is reference
	switch ReferenceType(x.ReferenceType) {
	case ReferenceTypeSingle, ReferenceTypeSlice, ReferenceTypeDynamic:
		// kind, typ, elem
		rs := referenceSchema{}
		rs.kind = reflect.Kind(x.Kind)
		rs.typ = ReferenceType(x.ReferenceType)
		if rs.typ != ReferenceTypeDynamic {
			if rs.elem, err = decodeSchema(x.Elem); err != nil {
				return
			}
		}
		s = &rs
		return
	case ReferenceTypeNone: // not a reference
	default:
		err = ErrInvalidEncodedSchema
		return
	}

	sc := schema{
		kind: reflect.Kind(x.Kind),
		name: x.Name,
	}

	switch k := reflect.Kind(x.Kind); k {
	case reflect.Slice:
		ss := sliceSchema{}
		ss.schema = sc
		if ss.elem, err = decodeSchema(x.Elem); err != nil {
			return
		}
		s = &ss
	case reflect.Array:
		as := arraySchema{}
		as.schema = sc
		as.length = int(x.Len)
		if as.elem, err = decodeSchema(x.Elem); err != nil {
			return
		}
		s = &as
	case reflect.Struct:
		ss := structSchema{}
		ss.schema = sc
		var f Field
		for _, ef := range x.Fields {
			if f, err = decodeField(ef); err != nil {
				return
			}
			ss.fields = append(ss.fields, f)
		}
		s = &ss
	default:
		s = &sc
	}

	return
}

func decodeField(b []byte) (f Field, err error) {
	var ef encodedField
	if err = encoder.DeserializeRaw(b, &ef); err != nil {
		return
	}
	ff := field{}
	ff.name = ef.Name
	ff.tag = ef.Tag
	if ff.schema, err = decodeSchema(ef.Schema); err != nil {
		return
	}
	f = &ff
	return
}

// encode

type registryEntity struct {
	Name   string
	Schema []byte
}

type registryEntities []registryEntity

// for sort.Sort

func (r registryEntities) Len() int {
	return len(r)
}

func (r registryEntities) Less(i, j int) bool {
	return r[i].Name < r[j].Name
}

func (r registryEntities) Swap(i, j int) {
	r[i], r[j] = r[j], r[i]
}
