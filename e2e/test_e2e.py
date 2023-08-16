import h5py
import json
import numpy as np
import os
import pytest
import shlex
import subprocess
import tempfile
import yaml
import re

from itertools import repeat
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple
from uuid import uuid4


@pytest.fixture
def data_dir():
    current_dir = Path(os.path.dirname(__file__))
    return current_dir/"data"


@pytest.fixture
def temp_output_filename():
    current_dir = Path(os.path.dirname(__file__))
    filename = str(current_dir/f"out_{uuid4().hex}.h5")
    yield filename
    Path(filename).unlink(missing_ok=True)


@pytest.fixture
def scanner_session_id():
    return str(uuid4())


def create_buffer() -> str:
    tyger_cmd = ["tyger", "buffer", "create"]
    output = subprocess.check_output(tyger_cmd)
    return output.decode("utf-8").strip()


def exec_recon(config_file: str, input_file: str, output_file: str, request: pytest.FixtureRequest, scan_name: Optional[str] = None, buffers: Optional[Dict[str, str]] = None, tags: Optional[Dict[str, str]] = None):
    clean_name = re.sub(r'[^a-zA-Z0-9_\-]', '_', request.node.name)

    tyger_args = ["tyger", "run", "exec", "--file", config_file, "--logs", "--baggage",
                  f"testName={request.node.name}", "--tag", f"testName={clean_name}"]
    if scan_name:
        tyger_args.extend(["--baggage", f"scan={scan_name}"])

    if buffers:
        for key, value in buffers.items():
            tyger_args.extend(["--buffer", f"{key}={value}"])

    if tags:
        for key, value in tags.items():
            tyger_args.extend(["--tag", f"{key}={value}"])

    print(tyger_args)

    to_stream_process = subprocess.Popen(["ismrmrd_hdf5_to_stream", "--use-stdout", "-i", input_file], stdout=subprocess.PIPE)
    tyger_process = subprocess.Popen(tyger_args, stdin=to_stream_process.stdout, stdout=subprocess.PIPE)
    to_hdf5_process = subprocess.Popen(["ismrmrd_stream_to_hdf5", "--use-stdin", "-g", "img", "-o", output_file], stdin=tyger_process.stdout)

    try:
        if tyger_process.wait(35*60) != 0:
            raise Exception(f"Run failed")
        if to_hdf5_process.wait(5*60) != 0:
            raise Exception(f"Failed to write HDF5 data")
    except subprocess.TimeoutExpired:
        raise Exception(f"Timed out waiting for run")
    finally:
        to_stream_process.kill()
        tyger_process.kill()
        to_hdf5_process.kill()


def get_dependencies():
    current_dir = Path(os.path.dirname(__file__))
    with open(current_dir/"../dependencies.json", "r") as read_file:
        deps = json.load(read_file)
        return deps['dependencies']


def get_dependency_image(dependency_name: str) -> str:
    deps = [d for d in get_dependencies() if d['name'] == dependency_name]
    if len(deps) != 1:
        raise RuntimeError("Error locating dependency")

    return f"{deps[0]['repository']}:{deps[0]['tag']}"


def verify_basic_recon_results(testdata_filename, recon_filename, image_variable_name: str = "image_0"):
    input_data: Any = h5py.File(str(testdata_filename))
    coil_images = input_data['dataset']['coil_images']
    coil_images = np.squeeze(coil_images['real'] + 1j*coil_images['imag'])
    recon_reference = np.abs(np.sqrt(np.sum(coil_images * np.conj(coil_images), 0)))
    ro_length = recon_reference.shape[-1]
    recon_reference = recon_reference[:, int(ro_length/4):int(ro_length/4+ro_length/2)]

    recon_data: Any = h5py.File(str(recon_filename))
    reconstruction = np.squeeze(recon_data['img'][image_variable_name]['data'])

    assert np.linalg.norm(reconstruction - recon_reference) / np.linalg.norm(recon_reference) < 2e-5


@pytest.fixture()
def configuration_generator():
    current_dir = Path(os.path.dirname(__file__))
    config_dir = current_dir/"config"
    files_generated = []

    def _config_generator(baseconfig: Path, image: str):
        temp_config_filename = str(current_dir/f"config_{uuid4().hex}.yml")
        with open(config_dir/baseconfig, 'r') as configfile:
            config: Dict = yaml.safe_load(configfile)

        config['job']['codespec']['image'] = image

        if 'worker' in config:
            config['worker']['codespec']['image'] = image

        with open(temp_config_filename, 'w') as tempconfigfile:
            yaml.safe_dump(config, tempconfigfile)
        files_generated.append(temp_config_filename)
        return temp_config_filename

    yield _config_generator

    for f in files_generated:
        Path(f).unlink()


@pytest.mark.parametrize(
    "image,config,image_variable_name",
    [
        (get_dependency_image("recon"), "basic_recon.yml", "image_0"),
        (get_dependency_image("python_recon"), "basic_recon.yml", "image_0"),
        (get_dependency_image("gadgetron"), "basic_gadgetron.yml", "image_0")
    ])
def test_simple_reconstruction(data_dir: Path, temp_output_filename: str, config: str, image: str, image_variable_name: str, configuration_generator, request):
    test_file = str(data_dir/"testdata.h5")

    with tempfile.NamedTemporaryFile(prefix=str(data_dir), suffix='.h5') as input_file_instance:  # Enable parallelism by duplicating the file
        subprocess.run(shlex.split(f'cp {test_file} {input_file_instance.name}'), check=True)  # Required due to limitation in ismrmrd.

        exec_recon(configuration_generator(config, image), input_file_instance.name, temp_output_filename, request)

        verify_basic_recon_results(test_file, temp_output_filename, image_variable_name)


@pytest.mark.parametrize(
    "config_file,image,input_file,output_image_variable_name,reference_file,reference_image_variable_name,tolerance",
    [
        ("distributed_gadgetron.yml", get_dependency_image("gadgetron"), Path("binning/binning.h5"),
         "img/image_2", Path("binning/binning_reference.h5"), "CMR_2DT_RTCine_KspaceBinning.xml/image_2", 0.01),

        ("grappa_gpu_gadgetron.yml", get_dependency_image("gadgetron"), Path("rt_grappa/rt_grappa.h5"),
         "img/image_0", Path("rt_grappa/grappa_rate2_out.mrd"), "grappa_float_cpu.xml/image_0", 0.05)
    ])
def test_reconstruction_against_reference(
        data_dir: Path,
        config_file: str,
        image: str,
        input_file: Path,
        output_image_variable_name: str,
        reference_file: str,
        reference_image_variable_name: str,
        temp_output_filename: str,
        tolerance: float,
        configuration_generator,
        request):
    config = configuration_generator(config_file, image)
    infile = str(data_dir/input_file)
    reffile = str(data_dir/reference_file)

    with tempfile.NamedTemporaryFile(prefix=str(data_dir), suffix='.h5') as input_file_instance:  # Enable parallelism by duplicating the file
        subprocess.run(shlex.split(f'cp {infile} {input_file_instance.name}'), check=True)  # Required due to limitation in ismrmrd.

        exec_recon(configuration_generator(config, image), input_file_instance.name, temp_output_filename, request)

        recon_data: Any = h5py.File(str(temp_output_filename))
        reconstruction = np.squeeze(recon_data[output_image_variable_name]['data'])

        with tempfile.NamedTemporaryFile(prefix=str(data_dir), suffix='.h5') as reference_file_instance:  # Enable parallelism by duplicating the file
            subprocess.run(shlex.split(f'cp {reffile} {reference_file_instance.name}'), check=True)  # Required due to limitation in ismrmrd.

            ref_data: Any = h5py.File(str(reference_file_instance.name))
            ref = np.squeeze(ref_data[reference_image_variable_name]['data'])

            assert np.linalg.norm(reconstruction.astype('float32') - ref.astype('float32')) / np.linalg.norm(ref.astype('float32')) < tolerance


@pytest.mark.parametrize(
    "image,configs,image_variable_name,scale",
    [
        (get_dependency_image("recon"), ("basic_noise.yml", "basic_recon_noise.yml"), "image_0", 1),
        (get_dependency_image("python_recon"), ("basic_noise.yml", "basic_recon_noise.yml"), "image_0", 1),
        (get_dependency_image("gadgetron"), ("gadgetron_noise.yml", "gadgetron_snr.yml"), "image_1", 10)
    ])
def test_noise_dependency_reconstruction(data_dir: Path, temp_output_filename: str, image: str, configs: Tuple[str, str], image_variable_name: str, scanner_session_id: str, configuration_generator, scale: float, request):
    noise_file = str(data_dir/"noise-scaling"/"data_1.h5")
    data_file = str(data_dir/"noise-scaling"/"data_2.h5")
    config_noise, config_recon = configs

    buffers: Dict[str, str] = {'noisecovariance': create_buffer()}
    with tempfile.NamedTemporaryFile(prefix=str(data_dir), suffix='.h5') as out_dummy:
        with tempfile.NamedTemporaryFile(prefix=str(data_dir), suffix='.h5') as input_file:  # Enable parallelism by duplicating the file
            subprocess.run(shlex.split(f'cp {noise_file} {input_file.name}'), check=True)  # Required due to limitation in ismrmrd.
            exec_recon(configuration_generator(config_noise, image), input_file.name, out_dummy.name, request, "noise", buffers=buffers)

    with tempfile.NamedTemporaryFile(prefix=str(data_dir), suffix='.h5') as input_file:  # Enable parallelism by duplicating the file
        subprocess.run(shlex.split(f'cp {data_file} {input_file.name}'), check=True)  # Required due to limitation in ismrmrd.
        exec_recon(configuration_generator(config_recon, image), input_file.name, temp_output_filename, request, "main", buffers=buffers)

    # Within the object being scanned, the standard deviation across repetitions
    # should be close to 1.
    recon_data: Any = h5py.File(temp_output_filename)
    img_data = np.squeeze(recon_data['img'][image_variable_name]['data'])
    img_mean = np.average(img_data, axis=0)
    img_std = np.std(img_data, axis=0)

    avg_relevant_std = np.mean(img_std[img_mean > np.max(img_mean)*0.25])

    assert np.abs(1 - avg_relevant_std) < 1e-2*scale


def get_cases():
    cases = []
    data_dir = os.path.join(os.path.dirname(__file__), 'data', 'gadgetron')
    with open(os.path.join(data_dir, 'testdata.json'), 'r') as test_data_file:
        test_data_description = json.load(test_data_file)

        for case in test_data_description['cases']:
            case_path = os.path.join(data_dir, case['case_file_path'])

            if os.path.isfile(case_path):
                with open(case_path, 'r') as case_file:
                    config: Dict = yaml.safe_load(case_file)
                    cases.append(config['case'])
            else:
                raise RuntimeError(f'Case file not found at {case_path}')

    return cases


cases = get_cases()
failing_cases = [
    'generic_cartesian_cine_denoise.cfg',  # ComplexToFloatGadget does not change header image type. Modify chain to use ImageFinish and/or fix gadget.
]
passing_cases = list(filter(lambda case: case['name'] not in failing_cases, cases))


@pytest.mark.parametrize(
    'test_case, image',
    list(zip(passing_cases, repeat(get_dependency_image("gadgetron")))),
    ids=[case['name'] for case in passing_cases]
)
def test_gadgetron_test_case(test_case, image, scanner_session_id, temp_output_filename, configuration_generator, request):
    buffers = {}
    if test_case.get('noise', None):
        buffers: Dict[str, str] = {'noisecovariance': create_buffer()}
        config = configuration_generator(test_case['noise']['run_file_path'], image)

        with tempfile.NamedTemporaryFile(prefix='/tmp/', suffix='.h5') as out_dummy:
            exec_recon(config, test_case['noise']['dat_file_path'], out_dummy.name, request, "noise", buffers=buffers)

    if test_case.get('main'):
        config = configuration_generator(test_case['main']['run_file_path'], image)
        exec_recon(config, test_case['main']['dat_file_path'], temp_output_filename, request, "main", buffers=buffers)

        def get_output_data(file_path, img_name):
            data: Any = h5py.File(file_path)
            array = np.squeeze(data['img'][img_name]['data'])
            return array.flatten().astype('float32')

        def get_reference_data(file_path, img_name):
            data: Any = h5py.File(file_path)
            key = img_name + "/data"
            array = np.squeeze(data[key])
            return array.flatten().astype('float32')

        images_to_validate = test_case['validation']['images'].items()
        assert len(images_to_validate) >= 1

        for ref_image, ref_img_meta in images_to_validate:
            img_no_prefix = ref_image[ref_image.rfind('/') + 1:]

            actual = get_output_data(temp_output_filename, img_no_prefix)
            reference = get_reference_data(ref_img_meta['reference_file_path'], ref_image)

            norm_diff = np.linalg.norm(np.subtract(actual, reference)) / np.linalg.norm(reference)
            scale = np.dot(actual, actual) / np.dot(actual, reference)

            assert norm_diff < float(ref_img_meta['value_comparison_threshold'])
            assert abs(1 - scale) < float(ref_img_meta['scale_comparison_threshold'])
