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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/ghodss/yaml"

	gitopsv1alpha1 "github.com/hybrid-cloud-patterns/patterns-operator/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

func TestParameterUnpacking(t *testing.T) {
	RegisterFailHandler(Fail)
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	logf.Log.Info("Running util test")
	parameters := []gitopsv1alpha1.PatternParameter{
		{
			Name:  "global.git.repo",
			Value: "https://github.com/some/place",
		},
		{
			Name:  "global.git.server",
			Value: "github.com",
		},
	}
	fmt.Printf("Converting values\n")
	out := ParametersToMap(parameters)
	out_s, err := yaml.Marshal(out)
	Expect(err).NotTo(HaveOccurred())
	fmt.Printf("Converted values:\n%s\n", out_s)
}

var _ = Describe("ExtractRepositoryName", func() {
	It("should extract the repository name from various URL formats", func() {
		testCases := []struct {
			inputURL     string
			expectedName string
		}{
			{"https://github.com/username/repo.git", "repo"},
			{"https://github.com/username/repo", "repo"},
			{"https://github.com/username/repo.git/", "repo"},
			{"https://github.com/username/repo/", "repo"},
			{"https://gitlab.com/username/my-project.git", "my-project"},
			{"https://gitlab.com/username/my-project", "my-project"},
			{"https://bitbucket.org/username/myrepo.git", "myrepo"},
			{"https://bitbucket.org/username/myrepo", "myrepo"},
			{"https://example.com/username/repo.git", "repo"},
			{"https://example.com/username/repo", "repo"},
			{"https://example.com/username/repo.git/", "repo"},
			{"https://example.com/username/repo/", "repo"},
		}

		for _, testCase := range testCases {
			repoName, err := extractRepositoryName(testCase.inputURL)
			Expect(err).To(BeNil())
			Expect(repoName).To(Equal(testCase.expectedName))
		}
	})

	It("should return an error for an invalid URL", func() {
		invalidURL := "invalid-url"
		_, err := extractRepositoryName(invalidURL)
		Expect(err).NotTo(BeNil())
	})
})
