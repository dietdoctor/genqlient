package test

// Code generated by github.com/Khan/genqlient, DO NOT EDIT.

import (
	"github.com/Khan/genqlient/graphql"
	"github.com/me/mypkg"
)

type QueryWithDoubleAliasResponse struct {
	User QueryWithDoubleAliasUser `json:"user"`
}

type QueryWithDoubleAliasUser struct {
	ID     mypkg.ID `json:"ID"`
	AlsoID mypkg.ID `json:"AlsoID"`
}

func QueryWithDoubleAlias(
	client graphql.Client,
) (*QueryWithDoubleAliasResponse, error) {
	var retval QueryWithDoubleAliasResponse
	err := client.MakeRequest(
		nil,
		"QueryWithDoubleAlias",
		`
query QueryWithDoubleAlias {
	user {
		ID: id
		AlsoID: id
	}
}
`,
		&retval,
		nil,
	)
	return &retval, err
}