from uuid import uuid4
import h5py
import numpy as np
import os
from pathlib import Path
import pytest
import random
import subprocess
from typing import Any, List, Tuple
from eminence_tools import get_dependency_image


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


def verify_basic_recon_results(testdata_filename, recon_filename, image_variable_name:str = "image_0"):
    input_data: Any = h5py.File(str(testdata_filename))
    coil_images = input_data['dataset']['coil_images']
    coil_images = np.squeeze(coil_images['real'] + 1j*coil_images['imag'])
    recon_reference = np.abs(np.sqrt(np.sum(coil_images * np.conj(coil_images), 0)))
    ro_length = recon_reference.shape[-1]
    recon_reference = recon_reference[:, int(ro_length/4):int(ro_length/4+ro_length/2)]

    recon_data: Any = h5py.File(str(recon_filename))
    reconstruction = np.squeeze(recon_data['img'][image_variable_name]['data'])

    assert np.linalg.norm(reconstruction - recon_reference) / np.linalg.norm(recon_reference) < 2e-5


@pytest.mark.parametrize(
    "image,recon_args,image_variable_name",
    [
        (get_dependency_image("recon"), [], "image_0"),
        (get_dependency_image("python_recon"), [], "image_0"),
        (get_dependency_image("gadgetron_recon"), ["-c", "default.xml"], "image_0")
    ])
def test_simple_reconstruction(data_dir: Path, temp_output_filename: str, image: str, recon_args: List[str], image_variable_name):
    test_file = str(data_dir/"testdata.h5")
    args = [
            "-f", test_file,
            "-o", temp_output_filename,
            "-i", image,
        ]

    if len(recon_args):
        args = args + ["--"] + recon_args

    run_eminence(args)
    verify_basic_recon_results(test_file, temp_output_filename, image_variable_name)

@pytest.mark.parametrize(
    "image,recon_args,image_variable_name",
    [
        (get_dependency_image("recon"), (["-p", "noise"], ["-p", "main"]), "image_0"),
        (get_dependency_image("python_recon"), (["-p", "noise"], ["-p", "main"]), "image_0"),
        (get_dependency_image("gadgetron_recon"), (["-c", "default_measurement_dependencies.xml"], ["-c", "Modified_Generic_Cartesian_FFT.xml"]), "image_1")
    ])
def test_noise_dependency_reconstruction(data_dir: Path, temp_output_filename: str, image: str, recon_args: Tuple[List[str], List[str]], image_variable_name: str, scanner_session_id: str):
    noise_file = str(data_dir/"noise-scaling"/"data_1.h5")
    data_file = str(data_dir/"noise-scaling"/"data_2.h5")
    recon_args_noise, recon_args_main = recon_args
    run_eminence(
        [
            "-f", noise_file,
            "-i", image,
            "-o", "out_dummy.h5",
            "-s", scanner_session_id,
        ] + ["--"] + recon_args_noise )

    Path("out_dummy.h5").unlink()

    run_eminence(
        [
            "-f", data_file,
            "-o", temp_output_filename,
            "-i", image,
            "-s", scanner_session_id
        ] + ["--"] + recon_args_main)

    # Within the object being scanned, the standard deviation across repetitions
    # should be close to 1.
    recon_data: Any = h5py.File(temp_output_filename)
    img_data = np.squeeze(recon_data['img'][image_variable_name]['data'])
    img_mean = np.average(img_data, axis=0)
    img_std = np.std(img_data, axis=0)

    avg_relevant_std = np.mean(img_std[img_mean > np.max(img_mean)*0.25])

    assert np.abs(1 - avg_relevant_std) < 1e-2
