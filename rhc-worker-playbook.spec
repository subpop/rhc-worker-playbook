%define _debugsource_template %{nil}
%define community_general_version 4.4.0
%define ansible_posix_version 1.3.0

Name:       rhc-worker-playbook
Version:    0.1.9
Release:    0%{?dist}
Summary:    Python worker for Red Hat connector that launches Ansible Runner
License:    GPL-2.0-or-later
URL:        https://github.com/redhatinsights/rhc-worker-playbook
Source:     rhc-worker-playbook-%{version}.tar.gz
Source1:    https://github.com/ansible-collections/community.general/archive/%{community_general_version}/ansible-collection-community-general-%{community_general_version}.tar.gz
Source2:    https://github.com/ansible-collections/ansible.posix/archive/%{ansible_posix_version}/ansible-collection-ansible-posix-%{ansible_posix_version}.tar.gz

%{?__python3:Requires: %{__python3}}
Requires: insights-client
Requires: python3dist(requests)
Requires: python3dist(pyyaml)
Requires: ansible-core
BuildRequires: pkgconfig
BuildRequires: python3-devel
BuildRequires: python3dist(pip)
BuildRequires: python3dist(wheel)
BuildRequires: python3dist(setuptools)
BuildRequires: openssl-devel
BuildRequires: c-ares-devel
BuildRequires: zlib-devel
BuildRequires: python3dist(cython)
BuildRequires: gcc
BuildRequires: gcc-c++

ExcludeArch:   i686

%description
Python-based worker for Red Hat connect, used to launch Ansible playbooks via Ansible Runner.

%prep
%setup -q -a1 -a2 -n %{name}-%{version}

pushd community.general-%{community_general_version}
rm -vr .github .azure-pipelines
rm -rvf tests/ hacking/
find -type f ! -executable -name '*.py' -print -exec sed -i -e '1{\@^#!.*@d}' '{}' +
find -type f -name '.gitignore' -print -delete
popd

pushd ansible.posix-%{ansible_posix_version}
rm -vr tests/{integration,utils} .github changelogs/fragments/.keep {test-,}requirements.txt shippable.yml
rm -vr .azure-pipelines
rm -rvf tests/
find -type f ! -executable -name '*.py' -print -exec sed -i -e '1{\@^#!.*@d}' '{}' +
find -type f -name '.gitignore' -print -delete
popd

%build
export GRPC_PYTHON_BUILD_WITH_CYTHON=True
export GRPC_PYTHON_BUILD_SYSTEM_OPENSSL=True
export GRPC_PYTHON_BUILD_SYSTEM_ZLIB=True
export GRPC_PYTHON_BUILD_SYSTEM_CARES=True
export GRPC_PYTHON_DISABLE_LIBC_COMPATIBILITY=True
%define rhc_config_dir %{_sysconfdir}/rhc-worker-playbook

%define _lto_cflags %{nil}
%set_build_flags
%{make_build} PREFIX=%{_prefix} LIBDIR=%{_libdir} CONFIG_DIR=%{rhc_config_dir} PYTHON_PKGDIR=%{python3_sitelib} build

# Building the Ansible Collections
pushd community.general-%{community_general_version}
tar -cf %{_tmppath}/community-general-%{community_general_version}.tar.gz .
popd

pushd ansible.posix-%{ansible_posix_version}
tar -cf %{_tmppath}/ansible-posix-%{ansible_posix_version}.tar.gz .
popd

%install
%{make_install} PREFIX=%{_prefix} LIBDIR=%{_libdir} CONFIG_DIR=%{rhc_config_dir} PYTHON_PKGDIR=%{python3_sitelib}

# Installing the Ansible Collections
mkdir -p %{buildroot}%{_datadir}/rhc-worker-playbook/ansible/collections/ansible_collections/community/general
mkdir -p %{buildroot}%{_datadir}/rhc-worker-playbook/ansible/collections/ansible_collections/ansible/posix

pushd %{buildroot}%{_datadir}/rhc-worker-playbook/ansible/collections/ansible_collections/community/general
tar -xf %{_tmppath}/community-general-%{community_general_version}.tar.gz
popd

pushd %{buildroot}%{_datadir}/rhc-worker-playbook/ansible/collections/ansible_collections/ansible/posix
tar -xf %{_tmppath}/ansible-posix-%{ansible_posix_version}.tar.gz
popd

# Creating the logs directory for ansible-runner
mkdir -p %{buildroot}%{_localstatedir}/log/rhc-worker-playbook/ansible/


%files
%{_libexecdir}/rhc/rhc-worker-playbook.worker
%{python3_sitelib}/rhc_worker_playbook/
%{python3_sitelib}/rhc_worker_playbook*.egg-info/
%{_libdir}/rhc-worker-playbook/
%{_datadir}/rhc-worker-playbook/ansible/collections/ansible_collections/
%{_localstatedir}/log/rhc-worker-playbook/ansible/
%config(noreplace) %{_sysconfdir}/rhc-worker-playbook/rhc-worker-playbook.toml

%doc

%changelog
