from typing import NamedTuple, Optional


class DependencyClientConfig(NamedTuple):
    configuration: str


class DependencySiemensConfig(NamedTuple):
    data_file: str
    measurement: str
    parameter_xsl: str = 'IsmrmrdParameterMap_Siemens.xsl'
    parameter_xml: str = 'IsmrmrdParameterMap_Siemens.xml'
    data_conversion_flag: str = ''


class ReconstructionClientConfig(NamedTuple):
    configuration: str
    additional_arguments: str = ''


class ReconstructionCopyConfig(NamedTuple):
    source: str


class ReconstructionSiemensConfig(NamedTuple):
    data_file: str
    measurement: str
    parameter_xsl: str = 'IsmrmrdParameterMap_Siemens.xsl'
    parameter_xml: str = 'IsmrmrdParameterMap_Siemens.xml'
    data_conversion_flag: str = ''


class ReconstructionTestConfig(NamedTuple):
    output_images: str
    reference_file: str
    reference_images: str
    scale_comparison_threshold: float = 0.01
    value_comparison_threshold: float = 0.01
    disable_image_header_test: bool = False
    disable_image_meta_test: bool = False


class RequirementConfig(NamedTuple):
    gpu_support: bool = False
    julia_support: bool = False
    matlab_support: bool = False
    python_support: bool = False
    gpu_memory: int = 0
    system_memory: int = 0


class DistributedConfig(NamedTuple):
    nodes: str


class TagsConfig(NamedTuple):
    tags: str


class GadgetronTestCase(NamedTuple):
    name: str
    dependency_client_config: Optional[DependencyClientConfig]
    dependency_siemens_config: Optional[DependencySiemensConfig]
    reconstruction_client_config: Optional[ReconstructionClientConfig]
    reconstruction_copy_config: Optional[ReconstructionCopyConfig]
    reconstruction_siemens_config: Optional[ReconstructionSiemensConfig]
    reconstruction_test_config: Optional[list[ReconstructionTestConfig]]
    requirements_config: Optional[RequirementConfig]
    distributed_config: Optional[DistributedConfig]
    tags_config: Optional[TagsConfig]
