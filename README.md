node-gsm-modem
==============

NodeJS module to control GSM modem connected to serial port.

Thanks to
=========

Emil Sedgh for [PDU.js](https://github.com/emilsedgh/pdu). I had to change it though.

Chris Williams for [node-serialport](https://github.com/voodootikigod/node-serialport)

Usage
=====
Install using npm:

    npm install --save gsm-modem

Require the module:

    var Modem = require('gsm-modem');

Connect to the modem:

    var modem1 = new Modem({
        ports : ['/dev/ttyUSB0', '/dev/ttyUSB1', '/dev/ttyUSB2']
    });
    modem1.connect(function (err) {
        if (err) {
            console.error('Error connecting modem: ', err);
            return;
        }
        // Start giving commands here...
    }

Connect options
---------------
__ports__: Array of serial ports to search for a modem. The module will try to connect to every ports specified and send _AT_ command. The first port modem answers to will be used as data/command port, others will be used for listening only. _Default: [/dev/ttyUSB0,/dev/ttyUSB1,/dev/ttyUSB2]_

__debug__: boolean if the module should output all data it sends/receives. _Default: false_

__auto_hangup__: boolean indicating whether modem should send _ATH (hangup)_ command when incoming call is received. _Default: false_

__commandTimeout__: milliseconds, how long to wait for response on any AT command. _Default: 15000_

__ussdTimeout__: milliseconds, how long to wait for USSD after sending the command. _Default: 15000_

__forever__: boolean, indicated whether module should run in daemon mode automatically handling modem connects and disconnects. _Default: false_

API
===
See wiki [page](https://github.com/paintenzero/node-gsm-modem/wiki/api).

Checked on modems
=================
* Huawei 3121S
* Huawei E171 / E173
* Huawei E1550
* Huawei K3765
* ZTE MF656A
* ZTE MF180
* Wavecom WISMO2C

TODO
====

* 8bit encoding
* Text mode
