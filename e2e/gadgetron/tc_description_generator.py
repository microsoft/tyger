import hashlib
import logging
import math
import os
import yaml

from typing import Dict

from config_types import GadgetronTestCase


class TestCaseDescriptionGenerator:
    def __init__(self, data_dir):
        self._data_dir = data_dir

    def generate(self, case: GadgetronTestCase, case_dir: str) -> Dict:
        file_dependencies = {}
        case_description = {}

        if case.dependency_client_config and not self._skip_dependency_for_case(case):
            file_dependencies.update(self._generate_noise_runfile(case, case_dir, case_description))

        if case.reconstruction_client_config:
            file_dependencies.update(self._generate_main_runfile(case, case_dir, case_description, case.dependency_client_config is not None))

        file_dependencies.update(self._generate_case_description_file(case, case_dir, case_description))

        return file_dependencies

    def _skip_dependency_for_case(self, case: GadgetronTestCase) -> bool:
        # There are a few Gadgetron cases that have dependcies listed,
        # but don't actually use them. This is a hack to skip those cases.
        if case.name == 'epi_2d.cfg':
            return True
        elif case.name == 'generic_nl_spirit_cartesian_sampling_cine.cfg':
            return True
        else:
            return False

    def _generate_noise_runfile(self, case: GadgetronTestCase, case_dir: str, case_description: Dict) -> Dict:
        noise_template_path = os.path.join(os.path.dirname(__file__), '../config/gadgetron_noise.yml')

        with open(noise_template_path, 'r') as file:
            config: Dict = yaml.safe_load(file)
            config['job']['codespec']['args'] = [case.dependency_client_config.configuration if arg ==
                                                 'default_measurement_dependencies.xml' else arg for arg in config['job']['codespec']['args']]

            noise_runfile_path = os.path.join(case_dir, 'run_noise.yml')
            with open(noise_runfile_path, 'w+') as run_file:
                logging.info(f'Generating {noise_runfile_path}')
                yaml.dump(config, run_file, default_flow_style=False)
                case_description.update({
                    'noise': {
                        'run_file_path': run_file.name,
                        'dat_file_path': os.path.join(case_dir, 'noise.h5')
                    }
                })

                return {os.path.relpath(os.path.normpath(noise_runfile_path),
                                        self._data_dir): hashlib.md5(open(noise_runfile_path, 'rb').read()).hexdigest()}

    def _generate_main_runfile(self, case: GadgetronTestCase, case_dir: str, case_description: Dict, noise_dependency: bool) -> Dict:
        main_template_path = os.path.join(os.path.dirname(__file__), '../config/gadgetron_default.yml')

        with open(main_template_path, 'r') as file:
            config: Dict = yaml.safe_load(file)
            config['job']['codespec']['args'] = [case.reconstruction_client_config.configuration if arg ==
                                                 'default.xml' else arg for arg in config['job']['codespec']['args']]

            if noise_dependency and self._skip_dependency_for_case(case) is False:
                config['job']['codespec']['buffers']['inputs'].append('noisecovariance')
                config['job']['codespec']['args'].extend(['--disable_storage', 'true', '--parameter', 'noisecovariancein=$(NOISECOVARIANCE_PIPE)'])

            config['job']['codespec']['resources'] |= {}
            config['job']['codespec']['resources']['requests'] |= {}
            config['job']['codespec']['resources']['limits'] |= {}

            if case.requirements_config.gpu_support:
                config['job']['codespec']['resources']['gpu'] = "1"

            memory = self._generate_mem_requirement(case.name, case.requirements_config.system_memory)

            config['job']['codespec']['resources']['requests']['memory'] = memory
            config['job']['codespec']['resources']['limits']['memory'] = memory

            main_runfile_path = os.path.join(case_dir, 'run_main.yml')

            if case.distributed_config:
                worker_description = {
                    'worker': {
                        'codespec': {
                            'image': 'eminencepublic.azurecr.io/gadgetron:current',
                            'args': [],
                            'resources': {
                                'requests': {
                                    'cpu': '3000m',
                                    'memory': memory
                                },
                                'limits': {
                                    'memory': memory
                                }
                            },
                            'endpoints': {
                                'gadgetron': 9002
                            }
                        },
                        'replicas': int(case.distributed_config.nodes),
                        'nodepool': 'cpunp',
                    }
                }

                config.update(worker_description)

                config['job']['codespec']['env'] = {
                    'GADGETRON_REMOTE_WORKER_COMMAND': 'printenv TYGER_GADGETRON_WORKER_ENDPOINT_ADDRESSES'
                }

            with open(main_runfile_path, 'w+') as run_file:
                logging.info(f'Generating {main_runfile_path}')
                yaml.dump(config, run_file, default_flow_style=False)
                case_description.update({
                    'main': {
                        'run_file_path': run_file.name,
                        'dat_file_path': os.path.join(case_dir, 'main.h5')
                    }
                })

                return {os.path.relpath(os.path.normpath(main_runfile_path),
                                        self._data_dir): hashlib.md5(open(main_runfile_path, 'rb').read()).hexdigest()}

    def _generate_case_description_file(self, case: GadgetronTestCase, case_dir: str, case_description: Dict) -> Dict:
        test_data_description = {
            'validation': {
                'images': {}
            }
        }

        for config in case.reconstruction_test_config:
            test_data_description['validation']['images'].update({
                config.reference_images: {
                    'reference_file_path': os.path.join(case_dir, config.reference_file),
                    'scale_comparison_threshold': config.scale_comparison_threshold,
                    'value_comparison_threshold': config.value_comparison_threshold
                }
            })

        case_description.update(test_data_description)

        case_description.update({'name': case.name})

        case_file_path = os.path.join(case_dir, 'case.yml')
        with open(case_file_path, 'w+') as file:
            logging.info(f'Generating {case_file_path}')
            yaml.dump({'case': case_description}, file, default_flow_style=False)

            return {os.path.relpath(os.path.normpath(case_file_path),
                                    self._data_dir): hashlib.md5(open(case_file_path, 'rb').read()).hexdigest()}

    def _generate_mem_requirement(self, case_name: str, mem_in_mb: str) -> str:
        memory_req_in_gb = math.ceil(int(mem_in_mb) / 1024.0)

        # HACK: These cases appear to take more memory than they state, so we give them more.
        # Proper fix would be to profile gadgetron's peak memory allocation during this reconstruction (say using valgrind's massif tool).
        # Then, perform big win optimizations, fix obvious leaks, and/or update the configs memory requirements if prudent.
        high_mem_cases = ['generic_grappa2x1_3d.cfg', 'generic_grappa2x2_3d.cfg', 'epi_2d.cfg',
                          'generic_spirit_cartesian_sampling_spat2.cfg', 'generic_rtcine_ai_landmark.cfg']

        if case_name in high_mem_cases:
            memory_req_in_gb += 4

        return str(memory_req_in_gb) + 'G'
