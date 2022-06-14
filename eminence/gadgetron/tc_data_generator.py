import hashlib
import logging
import os
import shlex
import shutil
import subprocess
import tempfile

from typing import Dict, Union

from config_types import GadgetronTestCase, ReconstructionSiemensConfig, DependencySiemensConfig


class TestCaseDataGenerator:
    def __init__(self, data_dir: str, cache_dir: str, md5_info: Dict):
        self._data_dir = data_dir
        self._data_cache_dir = cache_dir
        self._md5_info = md5_info
        self._converter_digest = hashlib.md5(open(str(shutil.which('siemens_to_ismrmrd')), 'rb').read()).hexdigest()

    def generate(self, case: GadgetronTestCase, case_dir: str) -> Dict:
        os.makedirs(case_dir, exist_ok=True)
        file_dependency_map = {}

        if case.dependency_siemens_config or case.reconstruction_siemens_config:
            file_dependency_map.update(self._convert_input_data(case, case_dir))

        if case.reconstruction_copy_config:
            target = os.path.join(self._data_cache_dir, case.reconstruction_copy_config.source)
            output_path = os.path.join(case_dir, 'main.h5')
            self._copy_data(target, output_path)

            file_dependency_map.update({
                    os.path.relpath(output_path, self._data_dir): self._get_md5_entry(case.reconstruction_copy_config.source)
                })

        if case.reconstruction_test_config:
            for config in case.reconstruction_test_config:
                target = os.path.join(self._data_cache_dir, config.reference_file)
                output_path = os.path.join(case_dir, config.reference_file)

                if not os.path.exists(output_path):
                    self._copy_data(target, output_path)

                file_dependency_map.update({os.path.relpath(output_path, self._data_dir): self._get_md5_entry(config.reference_file)})

        return file_dependency_map

    def calculate_output_data_signature(
            self,
            siemens_config: Union[ReconstructionSiemensConfig, DependencySiemensConfig],
            output_path: str) -> str:
        dat_file_path = os.path.join(self._data_cache_dir, siemens_config.data_file)

        command = self._generate_conversion_command(siemens_config, dat_file_path, output_path)
        matching_md5_entry = self._get_md5_entry(siemens_config.data_file)

        signature = hashlib.md5()
        signature.update(matching_md5_entry.encode())
        signature.update(hashlib.md5(command.encode()).hexdigest().encode())
        signature.update(self._converter_digest.encode())

        return signature.hexdigest()

    def _convert_input_data(self, case: GadgetronTestCase, case_dir: str) -> Dict:
        file_dependency_map = {}

        def _convert_and_update_dependency(
                config: Union[ReconstructionSiemensConfig,
                DependencySiemensConfig],
                command: str,
                output_path: str) -> Dict:
            with tempfile.TemporaryDirectory() as working_dir:
                self._run_in_working_dir(command, working_dir)
                file_dependency_map.update({
                        os.path.relpath(output_path, self._data_dir): self.calculate_output_data_signature(config, output_path)
                    })

        if case.dependency_siemens_config:
            dat_file_path = os.path.normpath(os.path.join(self._data_cache_dir, case.dependency_siemens_config.data_file))
            output_path = self._generate_output_path(case_dir, case.dependency_siemens_config)
            self._delete_if_exists(output_path)
            command = self._generate_conversion_command(case.dependency_siemens_config, dat_file_path, output_path)
            _convert_and_update_dependency(case.dependency_siemens_config, command, output_path)

        if case.reconstruction_siemens_config:
            dat_file_path = os.path.normpath(os.path.join(self._data_cache_dir, case.reconstruction_siemens_config.data_file))
            output_path = self._generate_output_path(case_dir, case.reconstruction_siemens_config)
            self._delete_if_exists(output_path)
            command = self._generate_conversion_command(case.reconstruction_siemens_config, dat_file_path, output_path)
            _convert_and_update_dependency(case.reconstruction_siemens_config, command, output_path)

        return file_dependency_map

    def _generate_output_path(self, case_dir: str, config: Union[ReconstructionSiemensConfig, DependencySiemensConfig]) -> str:
        if isinstance(config, ReconstructionSiemensConfig):
            return os.path.join(case_dir, 'main.h5')
        elif isinstance(config, DependencySiemensConfig):
            return os.path.join(case_dir, 'noise.h5')
        else:
            raise TypeError(f'{type(config)}')

    def _delete_if_exists(self, output_path: str):
        if os.path.exists(output_path):
            logging.warning(f'Deleting file at {output_path} to avoid conversion errors.')
            os.remove(output_path)

    def _generate_conversion_command(
            self,
            siemens_config: Union[ReconstructionSiemensConfig, DependencySiemensConfig],
            dat_file_path: str, output_path: str) -> str:
        return (
            f"siemens_to_ismrmrd -X "
            f"-f {dat_file_path} "
            f"-m {siemens_config.parameter_xml} "
            f"-x {siemens_config.parameter_xsl} "
            f"-o {output_path} "
            f"-z {siemens_config.measurement} "
            f"{siemens_config.data_conversion_flag} ")

    def _run_in_working_dir(self, command: str, working_dir: str):
        subprocess.run(shlex.split(command), stdout=subprocess.DEVNULL, check=True, cwd=working_dir)

    def _get_md5_entry(self, file: str) -> str:
        entry = list(filter(lambda info: info['file'] == file, self._md5_info))[0]
        return entry['md5']

    def _copy_data(self, target: str, output_path: str):
        os.makedirs(os.path.dirname(output_path), exist_ok=True)
        subprocess.check_call(shlex.split(f'cp {target} {output_path}'))
