/*
Copyright © 2022 SiteWhere LLC - All Rights Reserved
Unauthorized copying of this file, via any medium is strictly prohibited.
Proprietary and confidential.
*/

package graphql

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Khan/genqlient/graphql"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// Gets a GraphQL client based on command flags and other settings.
func GetGraphQLClientForCommand(cmd *cobra.Command, microservice string) graphql.Client {
	server, _ := cmd.Flags().GetString("server")
	instance, _ := cmd.Flags().GetString("instance")
	tenant, _ := cmd.Flags().GetString("tenant")
	url := fmt.Sprintf("http://%s/%s/%s/%s/graphql", server, instance, tenant, microservice)

	httpClient := http.Client{
		Timeout: time.Duration(1) * time.Second,
	}
	return graphql.NewClient(url, &httpClient)
}

// Beginning message for assure operation.
func assure(model string, token string) {
	fmt.Print(color.HiWhiteString(fmt.Sprintf("Assure %s '%s' exists: ", model, token)))
}

// Indicator for entity found.
func found(token string) {
	fmt.Println(color.HiGreenString("found"))
}

// Indicator for entity created.
func created(token string) {
	fmt.Println(color.HiGreenString("created"))
}
