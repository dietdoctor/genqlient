package generate

// This file implements the core type-generation logic of genqlient, whereby we
// traverse an operation-definition (and the schema against which it will be
// executed), and convert that into Go types.  It returns data structures
// representing the types to be generated; these are defined, and converted
// into code, in types.go.
//
// The entrypoints are convertOperation, which builds the response-type for a
// query, and convertInputType, which builds the argument-types.

import (
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// baseTypeForOperation returns the definition of the GraphQL type to which the
// root of the operation corresponds, e.g. the "Query" or "Mutation" type.
func (g *generator) baseTypeForOperation(operation ast.Operation) (*ast.Definition, error) {
	switch operation {
	case ast.Query:
		return g.schema.Query, nil
	case ast.Mutation:
		return g.schema.Mutation, nil
	case ast.Subscription:
		if !g.Config.AllowBrokenFeatures {
			return nil, errorf(nil, "genqlient does not yet support subscriptions")
		}
		return g.schema.Subscription, nil
	default:
		return nil, errorf(nil, "unexpected operation: %v", operation)
	}
}

// convertOperation builds the response-type into which the given operation's
// result will be unmarshaled.
func (g *generator) convertOperation(
	operation *ast.OperationDefinition,
	queryOptions *GenqlientDirective,
) (goType, error) {
	name := operation.Name + "Response"

	if def, ok := g.typeMap[name]; ok {
		return nil, errorf(operation.Position, "%s defined twice:\n%s", name, def)
	}

	baseType, err := g.baseTypeForOperation(operation.Operation)
	if err != nil {
		return nil, errorf(operation.Position, "%v", err)
	}

	goTyp, err := g.convertDefinition(
		name, operation.Name, baseType, operation.Position,
		operation.SelectionSet, queryOptions)

	if structType, ok := goTyp.(*goStructType); ok {
		// Override the ordinary description; the GraphQL documentation for
		// Query/Mutation is unlikely to be of value.
		// TODO(benkraft): This is a bit awkward/fragile.
		structType.Description =
			fmt.Sprintf("%v is returned by %v on success.", name, operation.Name)
		structType.Incomplete = false
	}

	return goTyp, err
}

var builtinTypes = map[string]string{
	// GraphQL guarantees int32 is enough, but using int seems more idiomatic
	"Int":     "int",
	"Float":   "float64",
	"String":  "string",
	"Boolean": "bool",
	"ID":      "string",
}

// typeName computes the name, in Go, that we should use for the given
// GraphQL type definition.  This is dependent on its location within the query
// (see DESIGN.md for more on why we generate type-names this way), which is
// determined by the prefix argument; the nextPrefix result should be passed to
// calls to typeName on any child types.
func (g *generator) typeName(prefix string, typ *ast.Definition) (name, nextPrefix string) {
	typeGoName := upperFirst(typ.Name)
	if typ.Kind == ast.Enum || typ.Kind == ast.InputObject {
		// If we're an enum or an input-object, there is only one type we
		// will ever possibly generate for this type, so we don't need any
		// of the qualifiers.  This is especially helpful because the
		// caller is very likely to need to reference these types in their
		// code.
		return typeGoName, typeGoName
	}

	name = prefix
	if !strings.HasSuffix(prefix, typeGoName) {
		// If the field and type names are the same, we can avoid the
		// duplication.  (We include the field name in case there are
		// multiple fields with the same type, and the type name because
		// that's the actual name (the rest are really qualifiers); but if
		// they are the same then including it once suffices for both
		// purposes.)
		name += typeGoName
	}

	if typ.Kind == ast.Interface || typ.Kind == ast.Union {
		// for interface/union types, we do not add the type name to the
		// name prefix; we want to have QueryFieldType rather than
		// QueryFieldInterfaceType.  So we just use the input prefix.
		return name, prefix
	}

	// Otherwise, the name will also be the prefix for the next type.
	return name, name
}

// convertInputType decides the Go type we will generate corresponding to an
// argument to a GraphQL operation.
func (g *generator) convertInputType(
	opName string,
	typ *ast.Type,
	options, queryOptions *GenqlientDirective,
) (goType, error) {
	// Sort of a hack: case the input type name to match the op-name.
	// TODO(benkraft): this is another thing that breaks the assumption that we
	// only need one of an input type, albeit in a relatively safe way.
	name := matchFirst(typ.Name(), opName)
	// note prefix is ignored here (see generator.typeName), as is selectionSet
	// (for input types we use the whole thing)).
	return g.convertType(name, "", typ, nil, options, queryOptions)
}

// convertType decides the Go type we will generate corresponding to a
// particular GraphQL type.  In this context, "type" represents the type of a
// field, and may be a list or a reference to a named type, with or without the
// "non-null" annotation.
func (g *generator) convertType(
	name, namePrefix string,
	typ *ast.Type,
	selectionSet ast.SelectionSet,
	options, queryOptions *GenqlientDirective,
) (goType, error) {
	if typ.Elem != nil {
		// Type is a list.
		elem, err := g.convertType(
			name, namePrefix, typ.Elem, selectionSet, options, queryOptions)
		return &goSliceType{elem}, err
	}

	// If this is a builtin type or custom scalar, just refer to it.
	def := g.schema.Types[typ.Name()]
	goTyp, err := g.convertDefinition(
		name, namePrefix, def, typ.Position, selectionSet, queryOptions)

	if options.GetPointer() {
		// Whatever we get, wrap it in a pointer.  (Because of the way the
		// options work, recursing here isn't as connvenient.)
		// Note this does []*T or [][]*T, not e.g. *[][]T.  See #16.
		goTyp = &goPointerType{goTyp}
	}
	return goTyp, err
}

// convertDefinition decides the Go type we will generate corresponding to a
// particular GraphQL named type.
//
// In this context, "definition" (and "named type") refer to an
// *ast.Definition, which represents the definition of a type in the GraphQL
// schema, which may be referenced by a field-type (see convertType).
func (g *generator) convertDefinition(
	name, namePrefix string,
	def *ast.Definition,
	pos *ast.Position,
	selectionSet ast.SelectionSet,
	queryOptions *GenqlientDirective,
) (goType, error) {
	qualifiedGoName, ok := g.Config.Scalars[def.Name]
	if ok {
		goRef, err := g.addRef(qualifiedGoName)
		return &goOpaqueType{goRef}, err
	}
	goBuiltinName, ok := builtinTypes[def.Name]
	if ok {
		return &goOpaqueType{goBuiltinName}, nil
	}

	switch def.Kind {
	case ast.Object:
		goType := &goStructType{
			GoName:      name,
			Description: def.Description,
			GraphQLName: def.Name,
			Fields:      make([]*goStructField, len(selectionSet)),
			Incomplete:  true,
		}
		g.typeMap[name] = goType

		for i, selection := range selectionSet {
			_, selectionDirective, err := g.parsePrecedingComment(
				selection, selection.GetPosition())
			if err != nil {
				return nil, err
			}
			selectionOptions := queryOptions.merge(selectionDirective)

			switch selection := selection.(type) {
			case *ast.Field:
				goType.Fields[i], err = g.convertField(
					namePrefix, selection, selectionOptions, queryOptions)
				if err != nil {
					return nil, err
				}
			case *ast.FragmentSpread:
				return nil, errorf(selection.Position, "not implemented: %T", selection)
			case *ast.InlineFragment:
				return nil, errorf(selection.Position, "not implemented: %T", selection)
			default:
				return nil, errorf(nil, "invalid selection type: %T", selection)
			}
		}
		return goType, nil

	case ast.InputObject:
		goType := &goStructType{
			GoName:      name,
			Description: def.Description,
			GraphQLName: def.Name,
			Fields:      make([]*goStructField, len(def.Fields)),
		}
		g.typeMap[name] = goType

		for i, field := range def.Fields {
			goName := upperFirst(field.Name)
			// Several of the arguments don't really make sense here:
			// - no field-specific options can apply, because this is
			//   a field in the type, not in the query (see also #14).
			// - namePrefix is ignored for input types; see note in
			//   generator.typeName.
			// TODO(benkraft): Can we refactor to avoid passing the values that
			// will be ignored?  We know field.Type is a scalar, enum, or input
			// type.  But plumbing that is a bit tricky in practice.
			fieldGoType, err := g.convertType(
				field.Type.Name(), "", field.Type, nil, queryOptions, queryOptions)
			if err != nil {
				return nil, err
			}

			goType.Fields[i] = &goStructField{
				GoName:      goName,
				GoType:      fieldGoType,
				JSONName:    field.Name,
				Description: field.Description,
			}
		}
		return goType, nil

	case ast.Interface, ast.Union:
		if !g.Config.AllowBrokenFeatures {
			return nil, errorf(pos, "not implemented: %v", def.Kind)
		}

		// We need to request __typename so we know which concrete type to use.
		hasTypename := false
		for _, selection := range selectionSet {
			field, ok := selection.(*ast.Field)
			if ok && field.Name == "__typename" {
				hasTypename = true
			}
		}
		if !hasTypename {
			// TODO(benkraft): Instead, modify the query to add __typename.
			return nil, errorf(pos, "union/interface type %s must request __typename", def.Name)
		}

		implementationTypes := g.schema.GetPossibleTypes(def)
		goType := &goInterfaceType{
			GoName:          name,
			Description:     def.Description,
			GraphQLName:     def.Name,
			Implementations: make([]*goStructType, len(implementationTypes)),
		}
		g.typeMap[name] = goType

		for i, implDef := range implementationTypes {
			implName, implNamePrefix := g.typeName(namePrefix, implDef)
			implTyp, err := g.convertDefinition(
				implName, implNamePrefix, implDef, pos, selectionSet, queryOptions)
			if err != nil {
				return nil, err
			}

			implStructTyp, ok := implTyp.(*goStructType)
			if !ok { // (should never happen on a valid schema)
				return nil, errorf(
					pos, "interface %s had non-object implementation %s",
					def.Name, implDef.Name)
			}
			goType.Implementations[i] = implStructTyp
		}
		return goType, nil

	case ast.Enum:
		goType := &goEnumType{
			GoName:      name,
			Description: def.Description,
			Values:      make([]goEnumValue, len(def.EnumValues)),
		}
		g.typeMap[name] = goType

		for i, val := range def.EnumValues {
			goType.Values[i] = goEnumValue{Name: val.Name, Description: val.Description}
		}
		return goType, nil

	case ast.Scalar:
		return nil, errorf(
			pos, "unknown scalar %v: please add it to genqlient.yaml", def.Name)
	default:
		return nil, errorf(pos, "unexpected kind: %v", def.Kind)
	}
}

// convertField converts a single GraphQL operation-field into a GraphQL type.
//
// Note that input-type fields are handled separately (inline in
// convertDefinition), because they come from the type-definition, not the
// operation.
func (g *generator) convertField(
	namePrefix string,
	field *ast.Field,
	fieldOptions, queryOptions *GenqlientDirective,
) (*goStructField, error) {
	if field.Definition == nil {
		// Unclear why gqlparser hasn't already rejected this,
		// but empirically it might not.
		return nil, errorf(
			field.Position, "undefined field %v", field.Alias)
	}

	// Needs to be exported for JSON-marshaling
	goName := upperFirst(field.Alias)

	typ := field.Definition.Type
	fieldTypedef := g.schema.Types[typ.Name()]

	// Note we don't deduplicate suffixes here -- if our prefix is GetUser and
	// the field name is User, we do GetUserUser.  This is important because if
	// you have a field called user on a type called User we need
	// `query q { user { user { id } } }` to generate two types, QUser and
	// QUserUser.  Note also this is named based on the GraphQL alias (Go
	// name), not the field-name, because if we have
	// `query q { a: f { b }, c: f { d } }` we need separate types for a and c,
	// even though they are the same type in GraphQL, because they have
	// different fields.
	name, namePrefix := g.typeName(namePrefix+goName, fieldTypedef)
	fieldGoType, err := g.convertType(
		name, namePrefix, typ, field.SelectionSet,
		fieldOptions, queryOptions)
	if err != nil {
		return nil, err
	}

	return &goStructField{
		GoName:      goName,
		GoType:      fieldGoType,
		JSONName:    field.Alias,
		Description: field.Definition.Description,
	}, nil
}