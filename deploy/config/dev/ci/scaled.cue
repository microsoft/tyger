package tyger

// Set the minumum node count to 1 on all node pools
_envs: [_environmentName]: clusters: [string]: userNodePools: [string]:	minCount: 1
