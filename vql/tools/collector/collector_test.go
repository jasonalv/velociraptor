package collector

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/Velocidex/ordereddict"
	"github.com/alecthomas/assert"
	"github.com/sebdah/goldie"
	"github.com/stretchr/testify/suite"
	"www.velocidex.com/golang/velociraptor/file_store/test_utils"
	flows_proto "www.velocidex.com/golang/velociraptor/flows/proto"
	"www.velocidex.com/golang/velociraptor/json"
	"www.velocidex.com/golang/velociraptor/logging"
	"www.velocidex.com/golang/velociraptor/reporting"
	"www.velocidex.com/golang/velociraptor/services"
	"www.velocidex.com/golang/velociraptor/third_party/zip"
	"www.velocidex.com/golang/velociraptor/utils"
	"www.velocidex.com/golang/vfilter"

	// Load all needed plugins
	_ "www.velocidex.com/golang/velociraptor/accessors/data"
	"www.velocidex.com/golang/velociraptor/vql/acl_managers"
	_ "www.velocidex.com/golang/velociraptor/vql/functions"
	_ "www.velocidex.com/golang/velociraptor/vql/networking"
	_ "www.velocidex.com/golang/velociraptor/vql/parsers"
	_ "www.velocidex.com/golang/velociraptor/vql/parsers/csv"
)

var (
	simpleCollectorArgs = &CollectPluginArgs{
		Artifacts: []string{"CollectionWithTypes"},
		Args: ordereddict.NewDict().
			Set("CollectionWithTypes", ordereddict.NewDict().

				// Bools will be converted to a "Y"
				Set("OffFlag", true).
				Set("ChoiceSelector", "InvalidChoice").
				Set("Flag", "N"). // Setting to "N" is the same as false.
				Set("Flag2", false).

				// Time object
				Set("StartDate", time.Unix(1608015035, 0)).

				// Int
				Set("StartDate2", 1608015035).

				// Float
				Set("StartDate3", 1608015035.0).

				// Stuffing rows data into a CSV field
				// should convert to csv.
				Set("CSVData", []*ordereddict.Dict{
					ordereddict.NewDict().
						Set("Foo", "Bar").
						Set("Baz", "Baz"),
					ordereddict.NewDict().
						Set("Foo", "Bar2").
						Set("Baz", "Baz2"),
				}).

				// Stuffing arbitrary data into a json
				// field should convert to json.
				Set("JSONData", []*ordereddict.Dict{
					ordereddict.NewDict().
						Set("Foo", "Bar").
						Set("Baz", "Baz"),
					ordereddict.NewDict().
						Set("Foo", "Bar2").
						Set("Baz", "Baz2"),
				}).

				// Add some spurious args, they are
				// not valid and will warn but be included.
				Set("InvalidArg", "InvalidArgValue")),
	}

	// Test case that adds a new artifact definition then calls it
	// with a new parameter.
	additionalArtifactCollectorArgs = ordereddict.NewDict().
					Set("artifacts", []string{"Custom.TestArtifactDependent"}).
					Set("args", ordereddict.NewDict().
						Set("Custom.TestArtifactDependent", ordereddict.NewDict().
				// This will be injected into the output zip by the below artifact.
				Set("FooVar", "HelloFooVar"))).
		Set("artifact_definitions", CustomTestArtifactDependent)

	CustomTestArtifactDependent = `
name: Custom.TestArtifactDependent
parameters:
- name: FooVar
sources:
- query: SELECT FooVar FROM scope()

reports:
- type: CLIENT
  template: |
     This is a template.
     {{ Query "SELECT * FROM source()" | Table }}
`
	customCollectionWithTypes = `
name: CollectionWithTypes
parameters:
- name: OffFlag
  type: bool
- name: ChoiceSelector
  type: choices
  default: First Choice
  choices:
      - First Choice
      - Second Choice
      - Third Choice

- name: Flag
  type: bool
  default: Y

- name: Flag2
  type: bool
  default: Y

- name: StartDate
  type: timestamp
- name: StartDate2
  type: timestamp
- name: StartDate3
  type: timestamp
- name: CSVData
  type: csv
- name: JSONData
  type: json_array
  default: "[]"

sources:
- query: |
      SELECT ChoiceSelector, Flag, Flag2,
             OffFlag, StartDate, StartDate2, StartDate3,
             CSVData, JSONData
      FROM scope()
`

	uploadArtifactCollectorArgs = ordereddict.NewDict().
					Set("artifacts", []string{"Custom.TestArtifactUpload"}).
					Set("artifact_definitions", `
name: Custom.TestArtifactUpload
sources:
- query: |
    SELECT upload(file="hello world", accessor="data", name="file.txt") AS Upload FROM scope()
`)
)

type TestSuite struct {
	test_utils.TestSuite
}

func (self *TestSuite) SetupTest() {
	self.ConfigObj = self.LoadConfig()
	self.LoadArtifacts([]string{customCollectionWithTypes})

	self.TestSuite.SetupTest()

	self.LoadArtifactFiles(
		"../../../artifacts/definitions/Demo/Plugins/GUI.yaml",
		"../../../artifacts/definitions/Reporting/Default.yaml",
	)

	Clock = &utils.MockClock{MockNow: time.Unix(1602103388, 0)}
	reporting.Clock = Clock
	launcher, err := services.GetLauncher(self.ConfigObj)
	assert.NoError(self.T(), err)
	launcher.SetFlowIdForTests("F.1234")

}

func (self *TestSuite) TestCollectionWithArtifacts() {
	output_file, err := ioutil.TempFile(os.TempDir(), "zip")
	assert.NoError(self.T(), err)
	output_file.Close()

	defer os.Remove(output_file.Name())

	report_file, err := ioutil.TempFile(os.TempDir(), "html")
	assert.NoError(self.T(), err)
	report_file.Close()
	defer os.Remove(report_file.Name())

	builder := services.ScopeBuilder{
		Config:     self.ConfigObj,
		ACLManager: acl_managers.NullACLManager{},
		Logger:     logging.NewPlainLogger(self.ConfigObj, &logging.FrontendComponent),
		Env:        ordereddict.NewDict(),
	}

	manager, err := services.GetRepositoryManager(self.ConfigObj)
	assert.NoError(self.T(), err)

	scope := manager.BuildScope(builder)
	defer scope.Close()

	additionalArtifactCollectorArgs.Set("output", output_file.Name())
	additionalArtifactCollectorArgs.Set("report", report_file.Name())

	results := []vfilter.Row{}
	for row := range (CollectPlugin{}).Call(context.Background(),
		scope, additionalArtifactCollectorArgs) {
		results = append(results, row)
	}

	zip_contents, err := openZipFile(output_file.Name())
	assert.NoError(self.T(), err)

	goldie.Assert(self.T(), "TestCollectionWithArtifacts",
		json.MustMarshalIndent(transformZipContent(self.T(), zip_contents)))
}

func (self *TestSuite) TestCollectionWithTypes() {
	output_file, err := ioutil.TempFile(os.TempDir(), "zip")
	assert.NoError(self.T(), err)
	output_file.Close()
	defer os.Remove(output_file.Name())

	builder := services.ScopeBuilder{
		Config:     self.ConfigObj,
		ACLManager: acl_managers.NullACLManager{},
		Logger:     logging.NewPlainLogger(self.ConfigObj, &logging.FrontendComponent),
		Env:        ordereddict.NewDict(),
	}

	manager, err := services.GetRepositoryManager(self.ConfigObj)
	assert.NoError(self.T(), err)

	scope := manager.BuildScope(builder)
	defer scope.Close()

	results := []vfilter.Row{}
	args := ordereddict.NewDict().
		Set("artifacts", simpleCollectorArgs.Artifacts).
		Set("output", output_file.Name()).
		Set("args", simpleCollectorArgs.Args)

	for row := range (CollectPlugin{}).Call(context.Background(),
		scope, args) {
		results = append(results, row)
	}

	zip_contents, err := openZipFile(output_file.Name())
	assert.NoError(self.T(), err)

	goldie.Assert(self.T(), "TestCollectionWithTypes",
		json.MustMarshalIndent(transformZipContent(self.T(), zip_contents)))
}

func (self *TestSuite) TestCollectionWithUpload() {
	output_file, err := ioutil.TempFile(os.TempDir(), "zip")
	assert.NoError(self.T(), err)
	output_file.Close()
	defer os.Remove(output_file.Name())

	builder := services.ScopeBuilder{
		Config:     self.ConfigObj,
		ACLManager: acl_managers.NullACLManager{},
		Logger:     logging.NewPlainLogger(self.ConfigObj, &logging.FrontendComponent),
		Env:        ordereddict.NewDict(),
	}

	manager, err := services.GetRepositoryManager(self.ConfigObj)
	assert.NoError(self.T(), err)

	scope := manager.BuildScope(builder)
	defer scope.Close()

	results := []vfilter.Row{}

	// Set the output file.
	uploadArtifactCollectorArgs.Set("output", output_file.Name())

	for row := range (CollectPlugin{}).Call(context.Background(),
		scope, uploadArtifactCollectorArgs) {
		results = append(results, row)
	}

	zip_contents, err := openZipFile(output_file.Name())
	assert.NoError(self.T(), err)

	serialized := json.MustMarshalIndent(ordereddict.NewDict().
		Set("zip_contents", zip_contents))
	goldie.Assert(self.T(), "TestCollectionWithUpload", serialized)
}

func openZipFile(name string) (*ordereddict.Dict, error) {
	result := ordereddict.NewDict()

	r, err := zip.OpenReader(name)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		serialized, err := ioutil.ReadAll(rc)
		if err != nil {
			return nil, err
		}

		rows, err := utils.ParseJsonToDicts(serialized)
		if err != nil {
			result.Set(f.Name, string(serialized))
			continue
		}

		result.Set(f.Name, rows)
	}

	return result, nil
}

func TestCollectorPlugin(t *testing.T) {
	suite.Run(t, &TestSuite{})
}

func transformZipContent(t *testing.T,
	zip_contents *ordereddict.Dict) *ordereddict.Dict {
	collection_context := &flows_proto.ArtifactCollectorContext{}
	serialized, _ := zip_contents.GetString("collection_context.json")
	err := json.Unmarshal([]byte(serialized), collection_context)
	assert.NoError(t, err)
	zip_contents.Set("collection_context.json", collection_context)
	return zip_contents
}
