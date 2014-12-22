var Modem = require('./');


function onSMS (modem, sms) {
    console.log('onSMS', sms);
}
function onStatusReport (modem, report) {
    console.log('onStatusReport', report);
}
function onDisconnect (modem) {
    console.log('onDisconnect');
}


var modem1 = new Modem({
    port : '/dev/ttyUSB0',
    notify_port : '/dev/ttyUSB1',
    onSMS : onSMS,
    onStatusReport : onStatusReport,
    onDisconnect : onDisconnect,
    balance_ussd : '*102*1#',
    dollar_regexp : /(-?\d+)\s*rub/,
    cents_regexp : /(-?\d+)\s*kop/,
    debug : true
});
modem1.connect(function () {

    modem1.getBalance (function(err, data){
        if (err===undefined)
            console.log ('balance:', data);
    });

    modem1.getAllSMS(function (err, sms) {
        console.log('SMSes:', sms);
    });


    modem1.sendSMS({
        receiver : 'ENTER YOUR NUMBER HERE',
        text : 'Проверка связи, однако!',
        request_status : true
    }, function (data) {
        console.log('sendSMS', data);
    });

    modem1.deleteAllSMS (function(err){
        if (err === undefined) {
            console.log ('all messages were deleted');
        } else {
            console.log ('messages were not deleted');
        }
    });

});

