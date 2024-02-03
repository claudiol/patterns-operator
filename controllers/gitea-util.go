/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"fmt"
	"net/http"

	"code.gitea.io/sdk/gitea"
)

// Function that creates a mirror repo in Gitea
func migrateGiteaRepo(username, password, upstreamURL, giteaServerRoute string) (bool, string, error) {
	option := gitea.SetBasicAuth(username, password)

	// We have to get the gitea client using the username and password provided
	giteaclient, err := gitea.NewClient(giteaServerRoute, option)
	if err != nil {
		return false, "", err
	}

	// Let's extract the repo name
	repoName, _ := extractRepositoryName(upstreamURL)

	// Check to see if the repo already exists
	repository, response, _ := giteaclient.GetRepo(Gitea_Admin_User, repoName)

	// Repo has been already migrated
	if response.Response.StatusCode == http.StatusOK {
		return true, repository.HTMLURL, nil
	}

	// Default description will include repo name and that it was created by
	// the Validated Patterns operator.
	descriptionFormat := "The [%s] repository was migrated by the Validated Patterns Operator."

	description := fmt.Sprintf(descriptionFormat, repoName)

	repository, _, err = giteaclient.MigrateRepo(gitea.MigrateRepoOption{
		CloneAddr: upstreamURL,
		RepoOwner: username,
		RepoName:  repoName,
		Mirror:    true, // We want gitea to periodically check for changes so create a mirror
		// Private:     true, // TODO: uncomment this if you want the repo to be private on gitea
		Description: description,
	})

	if err != nil {
		return false, "", err
	}

	return true, repository.HTMLURL, nil
}

func changeAdminPassword(client *gitea.Client, password string) (bool, error) {
	option := gitea.EditUserOption{
		LoginName: "gitea_admin",
		Password:  password,
	}

	response, err := client.AdminEditUser("gitea_admin", option)

	if err != nil {
		return false, err
	}
	fmt.Println(response)
	return true, nil
}
