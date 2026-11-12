#!/usr/bin/env python3
"""Native protobuf-only round trip, including the single reverse-call channel.

Use Python with protobuf==6.33.4 (scripts/requirements-smoke.txt).
No TUN is created and no system routes or DNS settings are changed.
"""
import ctypes
import itertools
import json
import os
import pathlib
import socket
import sys
import tempfile
import threading

ROOT=pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT/'tools/protocol'))
from libbox.v1 import control_pb2 as pb

library_path=pathlib.Path(sys.argv[1]).resolve()
# Cronet's purego loader searches the host executable directory and PATH (on
# Windows) or LD_LIBRARY_PATH (on Linux). Set this before Go initializes.
if sys.platform in ('win32','linux'):
    search_variable='PATH' if sys.platform=='win32' else 'LD_LIBRARY_PATH'
    os.environ[search_variable]=str(library_path.parent)+os.pathsep+os.environ.get(search_variable,'')
lib=ctypes.CDLL(str(library_path))
callback_type=ctypes.CFUNCTYPE(ctypes.c_int64,ctypes.c_void_p,ctypes.c_void_p,ctypes.c_uint64,ctypes.c_void_p,ctypes.c_uint64)
lib.libbox_session_new.argtypes=[ctypes.c_void_p,callback_type];lib.libbox_session_new.restype=ctypes.c_uint64
lib.libbox_session_call.argtypes=[ctypes.c_uint64,ctypes.c_void_p,ctypes.c_uint64,ctypes.POINTER(ctypes.c_uint64)];lib.libbox_session_call.restype=ctypes.c_void_p
lib.libbox_session_free.argtypes=[ctypes.c_uint64]
lib.libbox_free_buffer.argtypes=[ctypes.c_void_p]
seen=[];callback_errors=[]

@callback_type
def host(context,request,length,output,capacity):
    try:
        assert context==1234
        incoming=pb.HostRequest.FromString(ctypes.string_at(request,length))
        assert incoming.protocol_version==1 and incoming.timeout_ms>0
        kind=incoming.WhichOneof('method');seen.append(kind)
        reply=pb.HostResponse(protocol_version=1,id=incoming.id)
        if kind=='protect_socket': reply.error.CopyFrom(pb.Error(code='blocked',message='test protector denied the socket'))
        elif kind=='open_tun':
            assert incoming.open_tun.mtu==1500
            reply.tun.CopyFrom(pb.TunDescriptor(fd=-1))
        else: reply.error.CopyFrom(pb.Error(code='unsupported',message='test handler'))
        data=reply.SerializeToString();assert len(data)<=capacity
        ctypes.memmove(output,data,len(data));return len(data)
    except Exception as error:
        callback_errors.append(error);return -1

ids=itertools.count(1)
session=lib.libbox_session_new(1234,host)
def call(expected_error=None,**method):
    request=pb.Request(protocol_version=1,id=next(ids),timeout_ms=30000,**method)
    data=request.SerializeToString();buffer=ctypes.create_string_buffer(data);length=ctypes.c_uint64()
    pointer=lib.libbox_session_call(session,buffer,len(data),ctypes.byref(length))
    assert pointer and length.value<=4*1024*1024
    try: reply=pb.Response.FromString(ctypes.string_at(pointer,length.value))
    finally:lib.libbox_free_buffer(pointer)
    assert reply.protocol_version==1 and reply.id==request.id
    if expected_error: assert reply.error.code in expected_error,reply
    else: assert reply.WhichOneof('result')!='error',reply
    return reply

try:
    info=call(get_info=pb.Empty()).info
    if len(sys.argv)>2:
        expected=set(sys.argv[2].split(','))
        actual=set(info.build_tags.split(','))
        assert actual==expected, f'Build tags differ: missing={expected-actual}, extra={actual-expected}'
    with tempfile.TemporaryDirectory(prefix='libbox-protobuf-') as home:
        call(create_engine=pb.EngineOptions(working_directory=home))
        baseline=call(get_status=pb.Empty()).status.go_memory_limit_bytes
        config=json.dumps({'log':{'disabled':True},'outbounds':[{'type':'direct','tag':'direct'}]})
        call(start=pb.Config(config_json=config))
        assert call(get_status=pb.Empty()).status.state=='running'
        assert call(list_outbounds=pb.Empty()).outbounds.outbounds[0].tag=='direct'
        assert call(next_event=pb.NextEvent()).event.WhichOneof('data')=='state'
        call(destroy_engine=pb.Empty())
        if 'with_naive_outbound' in info.build_tags.split(','):
            call(create_engine=pb.EngineOptions(working_directory=home))
            naive=json.dumps({'log':{'disabled':True},'outbounds':[{'type':'naive','tag':'naive','server':'127.0.0.1','server_port':443,'tls':{'enabled':True,'server_name':'localhost'}}]})
            call(check_config=pb.Config(config_json=naive))
            call(start=pb.Config(config_json=naive))
            call(destroy_engine=pb.Empty())
        call(create_engine=pb.EngineOptions(working_directory=home,memory_profile=pb.EngineOptions.NETWORK_EXTENSION))
        assert call(get_status=pb.Empty()).status.go_memory_limit_bytes==min(baseline,40*1024*1024)
        call(destroy_engine=pb.Empty())
        if 'host_tun' in info.capabilities:
            call(create_engine=pb.EngineOptions(working_directory=home,network_mode=pb.EngineOptions.HOST,host_capabilities=[pb.EngineOptions.OPEN_TUN,pb.EngineOptions.PROTECT_SOCKET]))
            loop='lo0' if sys.platform=='darwin' else 'lo';index=socket.if_nametoindex(loop)
            call(update_network=pb.Network(default_interface=index,interfaces=[pb.Interface(index=index,name=loop,mtu=1500,addresses=['127.0.0.1/8'],loopback=True)]))
            call(start=pb.Config(config_json=config))
            call(expected_error={'host_error'},url_test=pb.URLTest(outbound='direct',url='http://127.0.0.1:1/',timeout_ms=1000))
            assert 'protect_socket' in seen
            call(stop=pb.Empty())
            tun=json.dumps({'log':{'disabled':True},'inbounds':[{'type':'tun','address':['172.19.0.1/30'],'mtu':1500}],'outbounds':[{'type':'direct','tag':'direct'}]})
            call(expected_error={'host_error'},start=pb.Config(config_json=tun))
            assert 'open_tun' in seen and not callback_errors
            call(destroy_engine=pb.Empty())
        call(create_engine=pb.EngineOptions(working_directory=home))
        assert call(get_status=pb.Empty()).status.go_memory_limit_bytes==baseline
        waiting=[]
        reader=threading.Thread(target=lambda:waiting.append(call(expected_error={'closed','invalid_handle'},next_event=pb.NextEvent(timeout_ms=60000))))
        reader.start();lib.libbox_session_free(session);reader.join(3)
        assert not reader.is_alive() and waiting
        session=0
finally:
    if session:lib.libbox_session_free(session)
print('Protobuf session and reverse callback smoke passed')
