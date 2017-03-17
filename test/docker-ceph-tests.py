#! /usr/bin/env python
import unittest
import subprocess

class DockerCephTests(unittest.TestCase):

    def test_docker_ceph_volume(self):
        out = run("docker run -t -v docker-test-volume:/bar:ceph debian:latest ls -l /bar")
        self.assertIn("lost+found", out)
        out = run("docker volume ls")
        self.assertIn("docker-test-volume", out)
        out = run("rbd showmapped")
        self.assertNotIn("docker-test-volume", out)
        out = run("rbd rm docker-test-volume")

    def test_docker_ceph_initialize_fs(self):
        out = run("rbd create --size=1G docker-test-volume")
        out = run("docker run -t -v docker-test-volume:/bar:ceph debian:latest ls -l /bar")
        self.assertIn("lost+found", out)
        out = run("rbd rm docker-test-volume")

    def test_docker_ceph_dont_initialize_fs(self):
        out = run("rbd create --size=1G docker-test-volume")
        dev = run("rbd map docker-test-volume 2>/dev/null").strip()
        out = run("mkfs.ext4 -m0 " + dev)
        tmpdir = run("mktemp -d ").strip()
        out = run("mount " + dev + " " + tmpdir)
        out = run("touch " + tmpdir + "/testfile")
        out = run("umount " + dev)
        out = run("rbd unmap docker-test-volume")
        out = run("docker run -t -v docker-test-volume:/bar:ceph debian:latest ls -l /bar")
        self.assertIn("testfile", out)
        out = run("rbd rm docker-test-volume")

    def test_docker_ceph_auto_resize(self):
        show_fs_size = "docker run -t -v docker-test-volume:/bar:ceph debian:latest df -h --output=size /bar"
        out = run("rbd create --size=1G docker-test-volume")
        out = run(show_fs_size)
        self.assertIn("976M", out)
        out = run("rbd resize --size=2G docker-test-volume")
        out = run(show_fs_size)
        self.assertIn("2.0G", out)
        out = run("rbd rm docker-test-volume")

    def test_docker_ceph_luks_volume(self):
        # create encripted luks volume
        out = run("rbd create --size=1G docker-test-volume")
        dev = run("rbd map docker-test-volume 2>/dev/null").strip()
        out = run("echo 'docker-test-volume' | cryptsetup luksFormat -q %s" % dev )
        out = run("rbd unmap docker-test-volume")

        create_file = "docker run -t -v docker-test-volume:/foo:ceph debian:latest /bin/bash -c \"echo 'dog' > /foo/cat\""
        out = run(create_file)
        read_file = "docker run -t -v docker-test-volume:/foo:ceph debian:latest cat /foo/cat"
        out = run(read_file)
        self.assertIn("dog", out)

        out = run("rbd showmapped")
        self.assertNotIn("docker-test-volume", out)

        out = run("rbd rm docker-test-volume")

def run(cmd):
    """
    Executes the given command returning a tuple with the return value and the stdout and stderr.
    :param cmd: the command to run
    :return: a tuple containing the return code and the output of the command execution
    """
    use_shell = isinstance(cmd, basestring)
    out = subprocess.check_output(cmd, stderr=subprocess.STDOUT, shell=use_shell, universal_newlines=True)
    return out


if __name__ == '__main__':
    unittest.main()
