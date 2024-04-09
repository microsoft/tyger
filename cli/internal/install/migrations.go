package install

type DatabaseVersion struct {
	Id          int    `json:"id"`
	Description string `json:"description"`
	State       string `json:"state"`
}
