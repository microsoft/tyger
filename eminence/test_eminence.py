from uuid import uuid4
import h5py
import numpy as np
import os
from pathlib import Path
import pytest
import random
import subprocess
from typing import Any, Dict, List, Tuple
from eminence_tools import get_dependency_image
import yaml


@pytest.fixture
def data_dir():
    current_dir = Path(os.path.dirname(__file__))
    return current_dir/"data"


@pytest.fixture
def temp_output_filename():
    current_dir = Path(os.path.dirname(__file__))
    filename = str(current_dir/f"out{random.randint(0,999)}.h5")
    yield filename
    Path(filename).unlink()


@pytest.fixture
def scanner_session_id():
    return str(uuid4())


def run_eminence(arguments: List[str], timeout_seconds=600):
    current_dir = Path(os.path.dirname(__file__))
    try:
        out = subprocess.check_output(["python", str(current_dir/"eminence.py")] + arguments, timeout=timeout_seconds, universal_newlines=True)
        return out.strip()
    except subprocess.CalledProcessError:
        print("Unable to call eminence")
        raise


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
        temp_config_filename = str(current_dir/f"config{random.randint(0,999)}.yml")
        with open(config_dir/baseconfig, 'r') as configfile:
            config: Dict = yaml.safe_load(configfile)

        config['tyger']['job']['image'] = image

        if 'worker' in config['tyger']:
            config['tyger']['worker']['image'] = image

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
        (get_dependency_image("gadgetron_recon"), "basic_gadgetron_recon.yml", "image_0")
    ])
def test_simple_reconstruction(data_dir: Path, temp_output_filename: str, config: str, image: str, image_variable_name: str, configuration_generator):
    test_file = str(data_dir/"testdata.h5")
    args = [
        "-f", test_file,
        "-o", temp_output_filename,
        "-r", configuration_generator(config, image),
        "-t", "30m"
    ]

    run_eminence(args)
    verify_basic_recon_results(test_file, temp_output_filename, image_variable_name)


@pytest.mark.parametrize(
    "config_file,image,input_file,output_image_variable_name,reference_file,reference_image_variable_name,tolerance",
    [
        ("distributed_gadgetron.yml", get_dependency_image("gadgetron_recon"), Path("binning/binning.h5"),
         "img/image_2", Path("binning/binning_reference.h5"), "CMR_2DT_RTCine_KspaceBinning.xml/image_2", 0.01),

        ("grappa_gpu_gadgetron.yml", get_dependency_image("gadgetron_recon"), Path("rt_grappa/rt_grappa.h5"),
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
        configuration_generator):
    config = configuration_generator(config_file, image)
    infile = str(data_dir/input_file)
    reffile = str(data_dir/reference_file)

    args = [
        "-f", infile,
        "-o", temp_output_filename,
        "-r", config,
        "-t", "30m"
    ]

    run_eminence(args)

    recon_data: Any = h5py.File(str(temp_output_filename))
    reconstruction = np.squeeze(recon_data[output_image_variable_name]['data'])

    ref_data: Any = h5py.File(str(reffile))
    ref = np.squeeze(ref_data[reference_image_variable_name]['data'])

    assert np.linalg.norm(reconstruction.astype('float32') - ref.astype('float32')) / np.linalg.norm(ref.astype('float32')) < tolerance


@pytest.mark.parametrize(
    "image,configs,image_variable_name",
    [
        (get_dependency_image("recon"), ("basic_noise.yml", "basic_recon.yml"), "image_0"),
        (get_dependency_image("python_recon"), ("basic_noise.yml", "basic_recon.yml"), "image_0"),
        (get_dependency_image("gadgetron_recon"), ("gadgetron_noise.yml", "gadgetron_snr.yml"), "image_1")
    ])
def test_noise_dependency_reconstruction(data_dir: Path, temp_output_filename: str, image: str, configs: Tuple[str, str], image_variable_name: str, scanner_session_id: str, configuration_generator):
    noise_file = str(data_dir/"noise-scaling"/"data_1.h5")
    data_file = str(data_dir/"noise-scaling"/"data_2.h5")
    config_noise, config_recon = configs
    run_eminence(
        [
            "-f", noise_file,
            "-r", configuration_generator(config_noise, image),
            "-o", "out_dummy.h5",
            "-s", scanner_session_id,
            "-t", "30m"
        ])

    Path("out_dummy.h5").unlink()

    run_eminence(
        [
            "-f", data_file,
            "-o", temp_output_filename,
            "-r", configuration_generator(config_recon, image),
            "-s", scanner_session_id,
            "-t", "30m"
        ])

    # Within the object being scanned, the standard deviation across repetitions
    # should be close to 1.
    recon_data: Any = h5py.File(temp_output_filename)
    img_data = np.squeeze(recon_data['img'][image_variable_name]['data'])
    img_mean = np.average(img_data, axis=0)
    img_std = np.std(img_data, axis=0)

    avg_relevant_std = np.mean(img_std[img_mean > np.max(img_mean)*0.25])

    assert np.abs(1 - avg_relevant_std) < 1e-2
