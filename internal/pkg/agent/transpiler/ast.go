// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License 2.0;
// you may not use this file except in compliance with the Elastic License 2.0.

package transpiler

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/cespare/xxhash/v2"

	"github.com/elastic/elastic-agent/internal/pkg/eql"
)

const (
	selectorSep = "."
	// conditionKey is the name of the reserved key that will be computed using EQL to a boolean result.
	//
	// This makes the key "condition" inside of a dictionary a reserved name.
	conditionKey = "condition"
)

// Selector defines a path to access an element in the Tree, currently selectors only works when the
// target is a Dictionary, accessing list values are not currently supported by any methods using
// selectors.
type Selector = string

var (
	trueVal  = []byte{1}
	falseVal = []byte{0}
)

// Processors represent an attached list of processors.
type Processors []map[string]interface{}

// Node represents a node in the configuration Tree a Node can point to one or multiples children
// nodes.
type Node interface {
	fmt.Stringer

	// Find search a string in the current node.
	Find(string) (Node, bool)

	// Value returns the value of the node.
	Value() interface{}

	//Close clones the current node.
	Clone() Node

	// ShallowClone makes a shallow clone of the node.
	ShallowClone() Node

	// Hash compute a sha256 hash of the current node and recursively call any children.
	Hash() []byte

	// Hash64With recursively computes the given hash for the Node and its children
	Hash64With(h *xxhash.Digest) error

	// Vars adds to the array with the variables identified in the node. Returns the array in-case
	// the capacity of the array had to be changed.
	Vars([]string, string) []string

	// Apply apply the current vars, returning the new value for the node. This does not modify the original Node.
	Apply(*Vars) (Node, error)

	// Processors returns any attached processors, because of variable substitution.
	Processors() Processors
}

// AST represents a raw configuration which is purely data, only primitives are currently supported,
// Int, float, string and bool. Complex are not taking into consideration. The Tree allow to define
// operation on the retrieves options in a more structured way. We are using this new structure to
// create filtering rules or manipulation rules to convert a configuration to another format.
type AST struct {
	root Node
}

func (a *AST) String() string {
	return "{AST:" + a.root.String() + "}"
}

// Dict represents a dictionary in the Tree, where each key is a entry into an array. The Dict will
// keep the ordering.
type Dict struct {
	value      []Node
	processors []map[string]interface{}
}

// NewDict creates a new dict with provided nodes.
func NewDict(nodes []Node) *Dict {
	return NewDictWithProcessors(nodes, nil)
}

// NewDictWithProcessors creates a new dict with provided nodes and attached processors.
func NewDictWithProcessors(nodes []Node, processors Processors) *Dict {
	return &Dict{nodes, processors}
}

// Find takes a string which is a key and try to find the elements in the associated K/V.
func (d *Dict) Find(key string) (Node, bool) {
	for _, i := range d.value {
		if i.(*Key).name == key {
			return i, true
		}
	}
	return nil, false
}

// Insert inserts a value into a collection.
func (d *Dict) Insert(node Node) {
	d.value = append(d.value, node)
}

func (d *Dict) String() string {
	var sb strings.Builder
	for i := 0; i < len(d.value); i++ {
		sb.WriteString("{")
		sb.WriteString(d.value[i].String())
		sb.WriteString("}")
		if i < len(d.value)-1 {
			sb.WriteString(",")
		}
	}
	return sb.String()
}

// Value returns the value of dict which is a slice of node.
func (d *Dict) Value() interface{} {
	return d.value
}

// Clone clones the values and return a new dictionary.
func (d *Dict) Clone() Node {
	nodes := make([]Node, 0, len(d.value))
	for _, i := range d.value {
		if i == nil {
			continue
		}
		nodes = append(nodes, i.Clone())

	}
	return &Dict{value: nodes}
}

// ShallowClone makes a shallow clone of the node.
func (d *Dict) ShallowClone() Node {
	nodes := make([]Node, 0, len(d.value))
	for _, i := range d.value {
		if i == nil {
			continue
		}
		// Dict nodes are key-value pairs, and we do want to make a copy of the key here
		nodes = append(nodes, i.ShallowClone())

	}
	return &Dict{value: nodes}
}

// Hash compute a sha256 hash of the current node and recursively call any children.
func (d *Dict) Hash() []byte {
	h := sha256.New()
	for _, v := range d.value {
		h.Write(v.Hash())
	}
	return h.Sum(nil)
}

// Hash64With recursively computes the given hash for the Node and its children
func (d *Dict) Hash64With(h *xxhash.Digest) error {
	for _, v := range d.value {
		if err := v.Hash64With(h); err != nil {
			return err
		}
	}
	return nil
}

// Vars returns a list of all variables referenced in the dictionary.
func (d *Dict) Vars(vars []string, defaultProvider string) []string {
	for _, v := range d.value {
		k := v.(*Key)
		vars = k.Vars(vars, defaultProvider)
	}
	return vars
}

// Apply applies the vars to all the nodes in the dictionary. This does not modify the original dictionary.
func (d *Dict) Apply(vars *Vars) (Node, error) {
	nodes := make([]Node, 0, len(d.value))
	for _, v := range d.value {
		k := v.(*Key)
		n, err := k.Apply(vars)
		if err != nil {
			return nil, err
		}
		if n == nil {
			continue
		}
		if k.name == conditionKey {
			b := n.Value().(*BoolVal)
			if !b.value {
				// condition failed; whole dictionary should be removed
				return nil, nil
			}
			// condition successful, but don't include condition in result
			continue
		}
		nodes = append(nodes, n)
	}
	return &Dict{nodes, nil}, nil
}

// Processors returns any attached processors, because of variable substitution.
func (d *Dict) Processors() Processors {
	if d.processors != nil {
		return d.processors
	}
	for _, v := range d.value {
		if p := v.Processors(); p != nil {
			return p
		}
	}
	return nil
}

// sort sorts the keys in the dictionary
func (d *Dict) sort() {
	sort.Slice(d.value, func(i, j int) bool {
		return d.value[i].(*Key).name < d.value[j].(*Key).name
	})
}

// Key represents a Key / value pair in the dictionary.
type Key struct {
	name      string
	value     Node
	condition *eql.Expression
}

// NewKey creates a new key with provided name node pair.
func NewKey(name string, val Node) *Key {
	return &Key{name: name, value: val}
}

func (k *Key) String() string {
	var sb strings.Builder
	sb.WriteString(k.name)
	sb.WriteString(":")
	if k.value == nil {
		sb.WriteString("nil")
	} else {
		sb.WriteString(k.value.String())
	}
	return sb.String()
}

// Find finds a key in a Dictionary or a list.
func (k *Key) Find(key string) (Node, bool) {
	switch v := k.value.(type) {
	case *Dict:
		return v.Find(key)
	case *List:
		return v.Find(key)
	default:
		return nil, false
	}
}

// Name returns the name for the key.
func (k *Key) Name() string {
	return k.name
}

// Value returns the raw value.
func (k *Key) Value() interface{} {
	return k.value
}

// Clone returns a clone of the current key and his embedded values.
func (k *Key) Clone() Node {
	if k.value != nil {
		return &Key{name: k.name, value: k.value.Clone()}
	}

	return &Key{name: k.name, value: nil}
}

// ShallowClone makes a shallow clone of the node.
func (k *Key) ShallowClone() Node {
	return &Key{name: k.name, value: k.value}
}

// Hash compute a sha256 hash of the current node and recursively call any children.
func (k *Key) Hash() []byte {
	h := sha256.New()
	h.Write([]byte(k.name))
	if k.value != nil {
		h.Write(k.value.Hash())
	}
	return h.Sum(nil)
}

// Hash64With recursively computes the given hash for the Node and its children
func (k *Key) Hash64With(h *xxhash.Digest) error {
	if _, err := h.WriteString(k.name); err != nil {
		return err
	}
	if k.value != nil {
		return k.value.Hash64With(h)
	}
	return nil
}

// Vars returns a list of all variables referenced in the value.
func (k *Key) Vars(vars []string, defaultProvider string) []string {
	if k.value == nil {
		return vars
	}
	return k.value.Vars(vars, defaultProvider)
}

// Apply applies the vars to the value. This does not modify the original node.
func (k *Key) Apply(vars *Vars) (Node, error) {
	if k.value == nil {
		return k, nil
	}
	if k.name == conditionKey {
		switch v := k.value.(type) {
		case *BoolVal:
			return k, nil
		case *StrVal:
			var err error
			if k.condition == nil {
				k.condition, err = eql.New(v.value)
				if err != nil {
					return nil, fmt.Errorf(`invalid condition "%s": %w`, v.value, err)
				}
			}
			cond, err := k.condition.Eval(vars, true)
			if err != nil {
				return nil, fmt.Errorf(`condition "%s" evaluation failed: %w`, v.value, err)
			}
			return &Key{name: k.name, value: NewBoolVal(cond)}, nil
		}
		return nil, fmt.Errorf("condition key's value must be a string; received %T", k.value)
	}
	v, err := k.value.Apply(vars)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return &Key{name: k.name, value: v}, nil
}

// Processors returns any attached processors, because of variable substitution.
func (k *Key) Processors() Processors {
	if k.value != nil {
		return k.value.Processors()
	}
	return nil
}

// List represents a slice in our Tree.
type List struct {
	value      []Node
	processors Processors
}

// NewList creates a new list with provided nodes.
func NewList(nodes []Node) *List {
	return NewListWithProcessors(nodes, nil)
}

// NewListWithProcessors creates a new list with provided nodes with processors attached.
func NewListWithProcessors(nodes []Node, processors Processors) *List {
	return &List{nodes, processors}
}

func (l *List) String() string {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < len(l.value); i++ {
		sb.WriteString(l.value[i].String())
		if i < len(l.value)-1 {
			sb.WriteString(",")
		}
	}
	sb.WriteString("]")
	return sb.String()
}

// Hash compute a sha256 hash of the current node and recursively call any children.
func (l *List) Hash() []byte {
	h := sha256.New()
	for _, v := range l.value {
		h.Write(v.Hash())
	}

	return h.Sum(nil)
}

// Hash64With recursively computes the given hash for the Node and its children
func (l *List) Hash64With(h *xxhash.Digest) error {
	for _, v := range l.value {
		if err := v.Hash64With(h); err != nil {
			return err
		}
	}
	return nil
}

// Find takes an index and return the values at that index.
func (l *List) Find(idx string) (Node, bool) {
	i, err := strconv.Atoi(idx)
	if err != nil {
		return nil, false
	}
	if l.value == nil {
		return nil, false
	}
	if i > len(l.value)-1 || i < 0 {
		return nil, false
	}

	return l.value[i], true
}

// Value returns the raw value.
func (l *List) Value() interface{} {
	return l.value
}

// Clone clones a new list and the clone items.
func (l *List) Clone() Node {
	nodes := make([]Node, 0, len(l.value))
	for _, i := range l.value {
		if i == nil {
			continue
		}
		nodes = append(nodes, i.Clone())
	}
	return &List{value: nodes}
}

// ShallowClone makes a shallow clone of the node.
func (l *List) ShallowClone() Node {
	nodes := make([]Node, 0, len(l.value))
	for _, i := range l.value {
		if i == nil {
			continue
		}
		nodes = append(nodes, i)
	}
	return &List{value: nodes}
}

// Vars returns a list of all variables referenced in the list.
func (l *List) Vars(vars []string, defaultProvider string) []string {
	for _, v := range l.value {
		vars = v.Vars(vars, defaultProvider)
	}
	return vars
}

// Apply applies the vars to all nodes in the list. This does not modify the original list.
func (l *List) Apply(vars *Vars) (Node, error) {
	nodes := make([]Node, 0, len(l.value))
	for _, v := range l.value {
		n, err := v.Apply(vars)
		if err != nil {
			return nil, err
		}
		if n == nil {
			continue
		}
		nodes = append(nodes, n)
	}
	return NewList(nodes), nil
}

// Processors returns any attached processors, because of variable substitution.
func (l *List) Processors() Processors {
	if l.processors != nil {
		return l.processors
	}
	for _, v := range l.value {
		if p := v.Processors(); p != nil {
			return p
		}
	}
	return nil
}

// StrVal represents a string.
type StrVal struct {
	value      string
	processors Processors
}

// NewStrVal creates a new string value node with provided value.
func NewStrVal(val string) *StrVal {
	return NewStrValWithProcessors(val, nil)
}

// NewStrValWithProcessors creates a new string value node with provided value and processors.
func NewStrValWithProcessors(val string, processors Processors) *StrVal {
	return &StrVal{val, processors}
}

// Find receive a key and return false since the node is not a List or Dict.
func (s *StrVal) Find(key string) (Node, bool) {
	return nil, false
}

func (s *StrVal) String() string {
	return s.value
}

// Value returns the value.
func (s *StrVal) Value() interface{} {
	return s.value
}

// Clone clone the value.
func (s *StrVal) Clone() Node {
	k := *s
	return &k
}

// ShallowClone makes a shallow clone of the node.
func (s *StrVal) ShallowClone() Node {
	return s.Clone()
}

// Hash we return the byte slice of the string.
func (s *StrVal) Hash() []byte {
	return []byte(s.value)
}

// Hash64With recursively computes the given hash for the Node and its children
func (s *StrVal) Hash64With(h *xxhash.Digest) error {
	_, err := h.WriteString(s.value)
	return err
}

// Vars returns a list of all variables referenced in the string.
func (s *StrVal) Vars(vars []string, defaultProvider string) []string {
	// errors are ignored (if there is an error determine the vars it will also error computing the policy)
	_, _ = replaceVars(s.value, func(variable string) (Node, Processors, bool) {
		vars = append(vars, variable)
		return nil, nil, false
	}, false, defaultProvider)
	return vars
}

// Apply applies the vars to the string value. This does not modify the original string.
func (s *StrVal) Apply(vars *Vars) (Node, error) {
	return vars.Replace(s.value)
}

// Processors returns any linked processors that are now connected because of Apply.
func (s *StrVal) Processors() Processors {
	return s.processors
}

// IntVal represents an int.
type IntVal struct {
	value      int
	processors Processors
}

// NewIntVal creates a new int value node with provided value.
func NewIntVal(val int) *IntVal {
	return NewIntValWithProcessors(val, nil)
}

// NewIntValWithProcessors creates a new int value node with provided value and attached processors.
func NewIntValWithProcessors(val int, processors Processors) *IntVal {
	return &IntVal{val, processors}
}

// Find receive a key and return false since the node is not a List or Dict.
func (s *IntVal) Find(key string) (Node, bool) {
	return nil, false
}

func (s *IntVal) String() string {
	return strconv.Itoa(s.value)
}

// Value returns the value.
func (s *IntVal) Value() interface{} {
	return s.value
}

// Clone clone the value.
func (s *IntVal) Clone() Node {
	k := *s
	return &k
}

// ShallowClone makes a shallow clone of the node.
func (s *IntVal) ShallowClone() Node {
	return s.Clone()
}

// Vars does nothing. Cannot have variable in an IntVal.
func (s *IntVal) Vars(vars []string, defaultProvider string) []string {
	return vars
}

// Apply does nothing.
func (s *IntVal) Apply(_ *Vars) (Node, error) {
	return s, nil
}

// Hash we convert the value into a string and return the byte slice.
func (s *IntVal) Hash() []byte {
	return []byte(s.String())
}

// Hash64With recursively computes the given hash for the Node and its children
func (s *IntVal) Hash64With(h *xxhash.Digest) error {
	_, err := h.WriteString(s.String())
	return err
}

// Processors returns any linked processors that are now connected because of Apply.
func (s *IntVal) Processors() Processors {
	return s.processors
}

// UIntVal represents an int.
type UIntVal struct {
	value      uint64
	processors Processors
}

// NewUIntVal creates a new uint value node with provided value.
func NewUIntVal(val uint64) *UIntVal {
	return NewUIntValWithProcessors(val, nil)
}

// NewUIntValWithProcessors creates a new uint value node with provided value with processors attached.
func NewUIntValWithProcessors(val uint64, processors Processors) *UIntVal {
	return &UIntVal{val, processors}
}

// Find receive a key and return false since the node is not a List or Dict.
func (s *UIntVal) Find(key string) (Node, bool) {
	return nil, false
}

func (s *UIntVal) String() string {
	return strconv.FormatUint(s.value, 10)
}

// Value returns the value.
func (s *UIntVal) Value() interface{} {
	return s.value
}

// Clone clone the value.
func (s *UIntVal) Clone() Node {
	k := *s
	return &k
}

// ShallowClone makes a shallow clone of the node.
func (s *UIntVal) ShallowClone() Node {
	return s.Clone()
}

// Hash we convert the value into a string and return the byte slice.
func (s *UIntVal) Hash() []byte {
	return []byte(s.String())
}

// Hash64With recursively computes the given hash for the Node and its children
func (s *UIntVal) Hash64With(h *xxhash.Digest) error {
	_, err := h.WriteString(s.String())
	return err
}

// Vars does nothing. Cannot have variable in an UIntVal.
func (s *UIntVal) Vars(vars []string, defaultProvider string) []string {
	return vars
}

// Apply does nothing.
func (s *UIntVal) Apply(_ *Vars) (Node, error) {
	return s, nil
}

// Processors returns any linked processors that are now connected because of Apply.
func (s *UIntVal) Processors() Processors {
	return s.processors
}

// FloatVal represents a float.
// NOTE: We will convert float32 to a float64.
type FloatVal struct {
	value      float64
	processors Processors
}

// NewFloatVal creates a new float value node with provided value.
func NewFloatVal(val float64) *FloatVal {
	return NewFloatValWithProcessors(val, nil)
}

// NewFloatValWithProcessors creates a new float value node with provided value with processors attached.
func NewFloatValWithProcessors(val float64, processors Processors) *FloatVal {
	return &FloatVal{val, processors}
}

// Find receive a key and return false since the node is not a List or Dict.
func (s *FloatVal) Find(key string) (Node, bool) {
	return nil, false
}

func (s *FloatVal) String() string {
	return fmt.Sprintf("%f", s.value)
}

// Value return the raw value.
func (s *FloatVal) Value() interface{} {
	return s.value
}

// Clone clones the value.
func (s *FloatVal) Clone() Node {
	k := *s
	return &k
}

// ShallowClone makes a shallow clone of the node.
func (s *FloatVal) ShallowClone() Node {
	return s.Clone()
}

// Hash return a string representation of the value, we try to return the minimal precision we can.
func (s *FloatVal) Hash() []byte {
	return []byte(s.hashString())
}

// Hash64With recursively computes the given hash for the Node and its children
func (s *FloatVal) Hash64With(h *xxhash.Digest) error {
	_, err := h.WriteString(s.hashString())
	return err
}

// hashString returns a string representation of s suitable for hashing.
func (s *FloatVal) hashString() string {
	return strconv.FormatFloat(s.value, 'f', -1, 64)
}

// Vars does nothing. Cannot have variable in an FloatVal.
func (s *FloatVal) Vars(vars []string, defaultProvider string) []string {
	return vars
}

// Apply does nothing.
func (s *FloatVal) Apply(_ *Vars) (Node, error) {
	return s, nil
}

// Processors returns any linked processors that are now connected because of Apply.
func (s *FloatVal) Processors() Processors {
	return s.processors
}

// BoolVal represents a boolean in our Tree.
type BoolVal struct {
	value      bool
	processors Processors
}

// NewBoolVal creates a new bool value node with provided value.
func NewBoolVal(val bool) *BoolVal {
	return NewBoolValWithProcessors(val, nil)
}

// NewBoolValWithProcessors creates a new bool value node with provided value with processors attached.
func NewBoolValWithProcessors(val bool, processors Processors) *BoolVal {
	return &BoolVal{val, processors}
}

// Find receive a key and return false since the node is not a List or Dict.
func (s *BoolVal) Find(key string) (Node, bool) {
	return nil, false
}

func (s *BoolVal) String() string {
	if s.value {
		return "true"
	}
	return "false"
}

// Value returns the value.
func (s *BoolVal) Value() interface{} {
	return s.value
}

// Clone clones the value.
func (s *BoolVal) Clone() Node {
	k := *s
	return &k
}

// ShallowClone makes a shallow clone of the node.
func (s *BoolVal) ShallowClone() Node {
	return s.Clone()
}

// Hash returns a single byte to represent the boolean value.
func (s *BoolVal) Hash() []byte {
	if s.value {
		return trueVal
	}
	return falseVal
}

// Hash64With recursively computes the given hash for the Node and its children
func (s *BoolVal) Hash64With(h *xxhash.Digest) error {
	var encodedBool []byte
	if s.value {
		encodedBool = trueVal
	} else {
		encodedBool = falseVal
	}
	_, err := h.Write(encodedBool)
	return err
}

// Vars does nothing. Cannot have variable in an BoolVal.
func (s *BoolVal) Vars(vars []string, defaultProvider string) []string {
	return vars
}

// Apply does nothing.
func (s *BoolVal) Apply(_ *Vars) (Node, error) {
	return s, nil
}

// Processors returns any linked processors that are now connected because of Apply.
func (s *BoolVal) Processors() Processors {
	return s.processors
}

// NewAST takes a map and convert it to an internal Tree, allowing us to executes rules on the
// data to shape it in a different way or to filter some of the information.
func NewAST(m map[string]interface{}) (*AST, error) {
	root, err := loadForNew(m)
	if err != nil {
		return nil, err
	}
	return &AST{root: root}, nil
}

func loadForNew(val interface{}) (Node, error) {
	root, err := load(reflect.ValueOf(val))
	if err != nil {
		return nil, fmt.Errorf("could not parse configuration into a tree, error: %w", err)
	}
	return root, nil
}

func load(val reflect.Value) (Node, error) {
	val = lookupVal(val)

	switch val.Kind() {
	case reflect.Map:
		return loadMap(val)
	case reflect.Slice, reflect.Array:
		return loadSliceOrArray(val)
	case reflect.String:
		return &StrVal{value: val.Interface().(string)}, nil
	case reflect.Int:
		return &IntVal{value: val.Interface().(int)}, nil
	case reflect.Int64:
		return &IntVal{value: int(val.Interface().(int64))}, nil
	case reflect.Uint:
		return &UIntVal{value: uint64(val.Interface().(uint))}, nil
	case reflect.Uint64:
		return &UIntVal{value: val.Interface().(uint64)}, nil
	case reflect.Float64:
		return &FloatVal{value: val.Interface().(float64)}, nil
	case reflect.Float32:
		return &FloatVal{value: float64(val.Interface().(float32))}, nil
	case reflect.Bool:
		return &BoolVal{value: val.Interface().(bool)}, nil
	default:
		if val.IsNil() {
			return nil, nil
		}
		return nil, fmt.Errorf("unknown type %T for %+v", val.Interface(), val)
	}
}

// Accept takes a visitor and will visit each node of the Tree while calling the right methods on
// the visitor.
// NOTE(ph): Some operation could be refactored to use a visitor, I plan to add a checksum visitor.
func (a *AST) Accept(visitor Visitor) {
	a.dispatch(a.root, visitor)
}

func (a *AST) dispatch(n Node, visitor Visitor) {
	switch t := n.(type) {
	case *Dict:
		visitorDict := visitor.OnDict()
		for _, child := range t.value {
			key := child.(*Key)
			visitorDict.OnKey(key.name)
			subvisitor := visitorDict.Visitor()
			a.dispatch(key.value, subvisitor)
			visitorDict.OnValue(subvisitor)
		}
		visitorDict.OnComplete()
	case *List:
		visitorList := visitor.OnList()
		for _, child := range t.value {
			subvisitor := visitorList.Visitor()
			a.dispatch(child, subvisitor)
			visitorList.OnValue(subvisitor)
		}
		visitorList.OnComplete()
	case *StrVal:
		visitor.OnStr(t.value)
	case *IntVal:
		visitor.OnInt(t.value)
	case *UIntVal:
		visitor.OnUInt(t.value)
	case *BoolVal:
		visitor.OnBool(t.value)
	case *FloatVal:
		visitor.OnFloat(t.value)
	}
}

// Clone clones the object.
func (a *AST) Clone() *AST {
	return &AST{root: a.root.Clone()}
}

// ShallowClone makes a shallow clone of the node.
func (a *AST) ShallowClone() *AST {
	return &AST{root: a.root.ShallowClone()}
}

// Hash calculates a hash from all the included nodes in the tree.
func (a *AST) Hash() []byte {
	return a.root.Hash()
}

// Hash64With recursively computes the given hash for the Node and its children
func (a *AST) Hash64With(h *xxhash.Digest) error {
	return a.root.Hash64With(h)
}

// HashStr return the calculated hash as a base64 url encoded string.
func (a *AST) HashStr() string {
	return base64.URLEncoding.EncodeToString(a.root.Hash())
}

// Equal check if two AST are equals by using the computed hash.
func (a *AST) Equal(other *AST) bool {
	if a.root == nil || other.root == nil {
		return a.root == other.root
	}
	hasher := xxhash.New()
	_ = a.Hash64With(hasher)
	thisHash := hasher.Sum64()
	hasher.Reset()
	_ = other.Hash64With(hasher)
	otherHash := hasher.Sum64()
	return thisHash == otherHash
}

// Lookup looks for a value from the AST.
//
// Return type is in the native form and not in the Node types from the AST.
func (a *AST) Lookup(name string) (interface{}, bool) {
	node, ok := Lookup(a, name)
	if !ok {
		return nil, false
	}
	_, isKey := node.(*Key)
	if isKey {
		// matched on a key, return the value
		node = node.Value().(Node)
	}

	m := &MapVisitor{}
	a.dispatch(node, m)

	return m.Content, true
}

func splitPath(s Selector) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, selectorSep)
}

func loadMap(val reflect.Value) (Node, error) {
	node := &Dict{}

	mapKeys := val.MapKeys()
	names := make([]string, 0, len(mapKeys))
	for _, aKey := range mapKeys {
		names = append(names, aKey.Interface().(string))
	}
	sort.Strings(names)

	for _, name := range names {
		aValue, err := load(val.MapIndex(reflect.ValueOf(name)))
		if err != nil {
			return nil, err
		}

		keys := strings.Split(name, selectorSep)
		if !isDictOrKey(aValue) {
			node.value = append(node.value, &Key{name: name, value: aValue})
			continue
		}

		// get last known existing node
		var lastKnownKeyIdx int
		var knownNode Node = node
		for i, k := range keys {
			n, isDict := knownNode.Find(k)
			if !isDict {
				break
			}

			lastKnownKeyIdx = i
			knownNode = n
		}

		// Produce remainder
		restKeys := keys[lastKnownKeyIdx+1:]
		restDict := &Dict{}
		if len(restKeys) == 0 {
			if avd, ok := aValue.(*Dict); ok {
				restDict.value = avd.value
			} else if avd, ok := aValue.(*Key); ok {
				restDict.value = []Node{avd.value}
			} else {
				restDict.value = append(restDict.value, aValue)
			}
		} else {
			for i := len(restKeys) - 1; i >= 0; i-- {
				if len(restDict.value) == 0 {
					// this is the first one
					restDict.value = []Node{&Key{name: restKeys[i], value: aValue}}
					continue
				}

				restDict.value = []Node{&Key{name: restKeys[i], value: restDict.Clone()}}
			}
		}

		// Attach remainder to last known node
		restKey := &Key{name: keys[lastKnownKeyIdx], value: restDict}
		if knownNodeDict, ok := knownNode.(*Dict); ok {
			knownNodeDict.value = append(knownNodeDict.value, restKey)
		} else if knownNodeKey, ok := knownNode.(*Key); ok {
			dict, ok := knownNodeKey.value.(*Dict)
			if ok {
				dict.value = append(dict.value, restDict.value...)
			}
		}
	}

	return node, nil
}

func isDictOrKey(val Node) bool {
	if _, ok := val.(*Key); ok {
		return true
	}
	if _, ok := val.(*Dict); ok {
		return true
	}
	return false
}

func loadSliceOrArray(val reflect.Value) (Node, error) {
	node := &List{}
	for i := 0; i < val.Len(); i++ {
		aValue, err := load(val.Index(i))
		if err != nil {
			return nil, err
		}
		node.value = append(node.value, aValue)
	}
	return node, nil
}

func lookupVal(val reflect.Value) reflect.Value {
	for (val.Kind() == reflect.Ptr || val.Kind() == reflect.Interface) && !val.IsNil() {
		val = val.Elem()
	}
	return val
}

func attachProcessors(node Node, processors Processors) Node {
	switch n := node.(type) {
	case *Dict:
		n.processors = processors
	case *List:
		n.processors = processors
	case *StrVal:
		n.processors = processors
	case *IntVal:
		n.processors = processors
	case *UIntVal:
		n.processors = processors
	case *FloatVal:
		n.processors = processors
	case *BoolVal:
		n.processors = processors
	}
	return node
}

// Lookup accept an AST and a selector and return the matching Node at that position.
func Lookup(a *AST, selector Selector) (Node, bool) {
	// Be defensive and ensure that the ast is usable.
	if a == nil || a.root == nil {
		return nil, false
	}

	// Run through the graph and find matching nodes.
	current := a.root
	for _, part := range splitPath(selector) {
		n, ok := current.Find(part)
		if !ok {
			return nil, false
		}
		current = n
	}

	return current, true
}

// Insert inserts an AST into an existing AST, will return and error if the target position cannot
// accept a new node.
func (a *AST) Insert(b *AST, to Selector) error {
	return Insert(a, b.root, to)
}

// Insert inserts a node into an existing AST, will return and error if the target position cannot
// accept a new node.
func Insert(a *AST, node Node, to Selector) error {
	current := a.root

	for _, part := range splitPath(to) {
		n, ok := current.Find(part)
		if !ok {
			switch t := current.(type) {
			case *Key:
				switch vt := t.value.(type) {
				case *Dict:
					newNode := &Key{name: part, value: &Dict{}}
					vt.value = append(vt.value, newNode)

					vt.sort()

					current = newNode
					continue
				case *List:
					// inserting at index but array empty
					newNode := &Dict{}
					vt.value = append(vt.value, newNode)

					current = newNode
					continue
				default:
					return fmt.Errorf("expecting collection and received %T for '%s'", to, to)
				}

			case *Dict:
				newNode := &Key{name: part, value: &Dict{}}
				t.value = append(t.value, newNode)

				t.sort()

				current = newNode
				continue
			default:
				return fmt.Errorf("expecting Dict and received %T for '%s'", t, to)
			}
		}

		current = n
	}

	// Apply the current node and replace any existing elements,
	// that could exist after the selector.
	d, ok := current.(*Key)
	if !ok {
		return fmt.Errorf("expecting Key and received %T for '%s'", current, to)
	}

	switch nt := node.(type) {
	case *Dict:
		d.value = node
	case *List:
		d.value = node
	case *Key:
		// adding key to existing dictionary
		// should overwrite the current key if it exists
		dValue, ok := d.value.(*Dict)
		if !ok {
			// not a dictionary (replace it all)
			d.value = &Dict{[]Node{node}, nil}
		} else {
			// remove the duplicate key (if it exists)
			for i, key := range dValue.value {
				if k, ok := key.(*Key); ok {
					if k.name == nt.name {
						dValue.value[i] = dValue.value[len(dValue.value)-1]
						dValue.value = dValue.value[:len(dValue.value)-1]
						break
					}
				}
			}
			// add the new key
			dValue.value = append(dValue.value, nt)
			dValue.sort()
		}
	default:
		d.value = &Dict{[]Node{node}, nil}
	}
	return nil
}

// Map transforms the AST into a map[string]interface{} and will abort and return any errors related
// to type conversion.
func (a *AST) Map() (map[string]interface{}, error) {
	m := &MapVisitor{}
	a.Accept(m)
	mapped, ok := m.Content.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("could not convert to map[string]iface, type is %T", m.Content)
	}
	return mapped, nil
}
