import configparser

from config_types import *


class DependencyClientConfigFactory:
    @staticmethod
    def construct(config: configparser.ConfigParser):
        try:
            return DependencyClientConfig(**config['dependency.client'])
        except KeyError:
            return None


class DependencySiemensConfigFactory:
    @staticmethod
    def construct(config: configparser.ConfigParser):
        try:
            return DependencySiemensConfig(**config['dependency.siemens'])
        except KeyError:
            return None


class ReconstructionClientConfigFactory:
    @staticmethod
    def construct(config: configparser.ConfigParser):
        try:
            return ReconstructionClientConfig(**config['reconstruction.client'])
        except KeyError:
            return None


class ReconstructionCopyConfigFactory:
    @staticmethod
    def construct(config: configparser.ConfigParser):
        try:
            return ReconstructionCopyConfig(**config['reconstruction.copy'])
        except KeyError:
            return None


class ReconstructionSiemensConfigFactory:
    @staticmethod
    def construct(config: configparser.ConfigParser):
        try:
            return ReconstructionSiemensConfig(**config['reconstruction.siemens'])
        except KeyError:
            return None


class ReconstructionTestConfigFactory:
    @staticmethod
    def construct(config: configparser.ConfigParser):
        try:
            configs = []

            for key in config.keys():
                if 'reconstruction.test' in key:
                    configs.append(ReconstructionTestConfig(**config[key]))

            return configs
        except KeyError:
            return None


class RequirementConfigFactory:
    @staticmethod
    def construct(config: configparser.ConfigParser):
        try:
            return RequirementConfig(**config['requirements'])
        except KeyError:
            return None


class DistributedConfigFactory:
    @staticmethod
    def construct(config: configparser.ConfigParser):
        try:
            return DistributedConfig(**config['distributed'])
        except KeyError:
            return None


class TagsConfigFactory:
    @staticmethod
    def construct(config: configparser.ConfigParser):
        try:
            return TagsConfig(**config['tags'])
        except KeyError:
            return None


class GadgetronTestCaseFactory:
    @staticmethod
    def construct(config, name):
        return GadgetronTestCase(
            name,
            DependencyClientConfigFactory.construct(config),
            DependencySiemensConfigFactory.construct(config),
            ReconstructionClientConfigFactory.construct(config),
            ReconstructionCopyConfigFactory.construct(config),
            ReconstructionSiemensConfigFactory.construct(config),
            ReconstructionTestConfigFactory.construct(config),
            RequirementConfigFactory.construct(config),
            DistributedConfigFactory.construct(config),
            TagsConfigFactory.construct(config))
