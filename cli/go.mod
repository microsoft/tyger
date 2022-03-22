module dev.azure.com/msresearch/compimag/_git/tyger/cli

go 1.17

require (
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v0.2.0
	github.com/AzureAD/microsoft-authentication-library-for-go v0.4.0
	github.com/andreyvit/diff v0.0.0-20170406064948-c7f18ee00883
	github.com/google/uuid v1.3.0
	github.com/johnstairs/pathenvconfig v0.2.0
	github.com/spf13/cobra v1.3.0
	github.com/stretchr/testify v1.7.0
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b
	k8s.io/apimachinery v0.23.5
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v0.20.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v0.8.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dlclark/regexp2 v1.4.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-jwt/jwt v3.2.1+incompatible // indirect
	github.com/inconshreveable/mousetrap v1.0.0 // indirect
	github.com/kr/pretty v0.3.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/pkg/browser v0.0.0-20210115035449-ce105d075bb4 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rogpeppe/go-internal v1.8.0 // indirect
	github.com/sergi/go-diff v1.2.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	golang.org/x/net v0.0.0-20211209124913-491a49abca63 // indirect
	golang.org/x/sys v0.0.0-20220114195835-da31bd327af9 // indirect
	golang.org/x/text v0.3.7 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
)

replace github.com/Azure/azure-sdk-for-go/sdk/storage/azblob => github.com/johnstairs/azure-sdk-for-go/sdk/storage/azblob v0.2.1-0.20211213213152-d710c4820679
