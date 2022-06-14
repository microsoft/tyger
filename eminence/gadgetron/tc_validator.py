import errno
import logging
import os

from typing import Dict, Union

from config_types import GadgetronTestCase, ReconstructionSiemensConfig, DependencySiemensConfig
from tc_data_generator import TestCaseDataGenerator
from dl_utils import validate_md5


class TestCaseValidator:
    def __init__(self, data_generator: TestCaseDataGenerator, data_dir: str):
        self._data_dir = data_dir
        self._data_generator = data_generator

    def validate(self, case: GadgetronTestCase, path: str, case_description: Dict):
        for relpath, _ in case_description['file_dependencies'].items():
            self._validate_exists(os.path.join(self._data_dir, relpath))

        self._validate_case_file(case_description)

        if case.dependency_siemens_config:
            self._validate_noise_files(case, path, case_description)

        if case.reconstruction_siemens_config or case.reconstruction_copy_config:
            self._validate_main_files(case, path, case_description)

        if case.reconstruction_test_config:
            self._validate_reference_files(case, path, case_description)

        logging.info(f'Validated data for case: {case.name}')

    def _validate_exists(self, file_path: str):
        if not os.path.exists(file_path):
            raise FileNotFoundError(errno.ENOENT, os.strerror(errno.ENOENT), file_path)

    def _validate_case_file(self, case_description: Dict):
        validate_md5(
            os.path.join(self._data_dir, case_description['case_file_path']),
            case_description['file_dependencies'][case_description['case_file_path']])

    def _validate_noise_files(self, case: GadgetronTestCase, path: str, case_description: Dict):
        dat_file_path = os.path.join(path, 'noise.h5')
        md5_key = os.path.relpath(dat_file_path, self._data_dir)
        self._validate_converted_data_signature(
            case.dependency_siemens_config,
            dat_file_path,
            case_description['file_dependencies'][md5_key])

        run_noise_path = os.path.join(path, 'run_noise.yml')
        expected_run_noise_md5 = case_description['file_dependencies'][os.path.relpath(run_noise_path, self._data_dir)]
        validate_md5(run_noise_path, expected_run_noise_md5)

    def _validate_main_files(self, case: GadgetronTestCase, path: str, case_description: Dict):
        if case.reconstruction_siemens_config:
            dat_file_path = os.path.join(path, 'main.h5')
            md5_key = os.path.relpath(dat_file_path, self._data_dir)
            self._validate_converted_data_signature(
                case.reconstruction_siemens_config,
                dat_file_path,
                case_description['file_dependencies'][md5_key])

            run_main_path = os.path.join(path, 'run_main.yml')
            expected_run_main_md5 = case_description['file_dependencies'][os.path.relpath(run_main_path, self._data_dir)]
            validate_md5(run_main_path, expected_run_main_md5)

        if case.reconstruction_copy_config:
            dat_file_path = os.path.join(path, 'main.h5')
            md5_key = os.path.relpath(dat_file_path, self._data_dir)
            validate_md5(dat_file_path, case_description['file_dependencies'][md5_key])

            run_main_path = os.path.join(path, 'run_main.yml')
            expected_run_main_md5 = case_description['file_dependencies'][os.path.relpath(run_main_path, self._data_dir)]
            validate_md5(run_main_path, expected_run_main_md5)

    def _validate_reference_files(self, case: GadgetronTestCase, path: str, case_description: Dict):
        for config in case.reconstruction_test_config:
            dat_file_path = os.path.join(path, config.reference_file)
            md5_key = os.path.relpath(dat_file_path, self._data_dir)
            validate_md5(dat_file_path, case_description['file_dependencies'][md5_key])

    def _validate_converted_data_signature(
            self,
            siemens_config: Union[ReconstructionSiemensConfig, DependencySiemensConfig],
            output_path: str, expected_signature: str):
        actual_signature = self._data_generator.calculate_output_data_signature(siemens_config, output_path)
        if expected_signature != actual_signature:
            raise ValueError(f'{output_path}: Mismatched signatures, expected {expected_signature}, actual: {actual_signature}')
        else:
            logging.debug(f'{output_path}: signature match')
