//TODO: make 8-bit encoding

var pduParser = {};

var sevenBitDefault = new Array('@', '£', '$', '¥', 'è', 'é', 'ù', 'ì', 'ò', 'Ç', '\n', 'Ø', 'ø', '\r','Å', 'å','\u0394', '_', '\u03a6', '\u0393', '\u039b', '\u03a9', '\u03a0','\u03a8', '\u03a3', '\u0398', '\u039e','\x1b', 'Æ', 'æ', 'ß', 'É', ' ', '!', '"', '#', '¤', '%', '&', '\'', '(', ')','*', '+', ',', '-', '.', '/', '0', '1', '2', '3', '4', '5', '6', '7','8', '9', ':', ';', '<', '=', '>', '?', '¡', 'A', 'B', 'C', 'D', 'E','F', 'G', 'H', 'I', 'J', 'K', 'L', 'M', 'N', 'O', 'P', 'Q', 'R', 'S','T', 'U', 'V', 'W', 'X', 'Y', 'Z', 'Ä', 'Ö', 'Ñ', 'Ü', '§', '¿', 'a','b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o','p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z', 'ä', 'ö', 'ñ','ü', 'à');
var sevenBitEsc = new Array('', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '^', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '{', '}', '', '', '', '', '', '\\', '', '', '', '', '', '', '', '', '', '', '', '', '[', '~', ']',
    '', '|', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '€', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '');

//http://en.wikipedia.org/wiki/GSM_03.40
//http://smsconnexion.wordpress.com/2009/02/12/sms-pdu-formats-demystified/
pduParser.parse = function(pdu) {
    //Cursor points to the last octet we've read.
    var cursor = 0;

    var obj = parseSMSCPart (pdu);
    obj.smsc_tpdu = pdu;
    cursor += obj.length;

    var buffer = new Buffer(pdu.slice(cursor,cursor+6), 'hex');
    cursor += 6;
    var smsDeliver = parseInt(buffer[0]);

    var smsDeliverBits = ("00000000"+parseInt(smsDeliver).toString(2)).slice(-8);
    var tp_mti = smsDeliverBits.slice(-2);
    obj.tpdu_type = TP_MTI_To_String (tp_mti);

    if (tp_mti=='10'){ //SMS-STATUS-REPORT
    	return this.parseStatusReport (pdu.slice(cursor-6), obj);
    }
    var udhi =  smsDeliverBits.slice(1,2) === "1";

    var senderSize = buffer[1];
    if(senderSize % 2 === 1)
        senderSize++;

    obj.sender_type = parseInt(buffer[2]).toString(16);
    if (obj.sender_type === 'd0') {
        obj.sender = this.decode7Bit(pdu.slice(cursor, cursor+senderSize), Math.floor(senderSize*4/7)).trim();
    } else {
        obj.sender = pduParser.deSwapNibbles(pdu.slice(cursor, cursor+senderSize));
    }
    cursor += senderSize;

    var protocolIdentifier = pdu.slice(cursor, cursor+2);
    cursor += 2;

    var dataCodingScheme = pdu.slice(cursor, cursor+2);
    cursor = cursor+2;

    obj.dcs = parseInt(dataCodingScheme, 16);
    obj.encoding = pduParser.detectEncoding(dataCodingScheme);


    obj.time = parseTS (pdu.slice(cursor, cursor+14));

    cursor += 14;

    var dataLength = parseInt(pdu.slice(cursor, cursor+2), 16).toString(10);
    cursor += 2;

    if(udhi) { //User-Data-Header-Indicator: means there's some User-Data-Header.
        var udhLength = pdu.slice(cursor, cursor+2);
        var iei = pdu.slice(cursor+2, cursor+4);
        var headerLength, referenceNumber, parts, currentPart;
        if(iei == "00") { //Concatenated sms.
            headerLength = pdu.slice(cursor+4, cursor+6);
            referenceNumber = pdu.slice(cursor+6, cursor+8);
            parts = pdu.slice(cursor+8, cursor+10);
            currentPart = pdu.slice(cursor+10, cursor+12);
        }

        if(iei == "08") { //Concatenaded sms with a two-bytes reference number
            headerLength = pdu.slice(cursor+4, cursor+6);
            referenceNumber = pdu.slice(cursor+6, cursor+10);
            parts = pdu.slice(cursor+10, cursor+12);
            currentPart = pdu.slice(cursor+12, cursor+14);
        }

        /*if(iei == '00')
            cursor += (udhLength-2)*4;
        else if(iei == '08')
            cursor += ((udhLength-2)*4)+2;
        else
            cursor += (udhLength-2)*2;*/
        cursor = cursor + (parseInt(udhLength, 16) + 1) * 2;
    }
    if(obj.encoding === '16bit')
        var text = pduParser.decode16Bit(pdu.slice(cursor), dataLength);
    else if(obj.encoding === '7bit')
        if (udhi && iei=='00') var text = pduParser.decode7Bit(pdu.slice(cursor), dataLength-7, 1); //If iei ==0, then there is some unpadding to do
        else if (udhi && iei=='08') var text = pduParser.decode7Bit(pdu.slice(cursor), dataLength-8); //If no udhi or iei = 08 then no unpadding to do
        else var text = pduParser.decode7Bit(pdu.slice(cursor), dataLength);
    else if(obj.encoding === '8bit')
        var text = ''; //TODO

    obj.text = text;

    if(udhi) {
        obj['udh'] = {
            'length' : udhLength,
            'iei' : iei,
        };

        if(iei == '00' || iei == '08') {
            obj['udh']['reference_number'] = referenceNumber;
            obj['udh']['parts'] = parseInt(parts);
            obj['udh']['current_part'] = parseInt(currentPart);
        }
    }

    return obj;
}

pduParser.detectEncoding = function(dataCodingScheme) {
    if (typeof dataCodingScheme === 'string') dataCodingScheme = parseInt(dataCodingScheme, 16);
    var binary = ('00000000'+(dataCodingScheme.toString(2))).slice(-8);
    if(binary == '00000000')
        return '7bit';

    // if(binary.slice(0, 2) === '00') {
        var compressed = binary.slice(2, 1) === '1';
        var bitsHaveMeaning = binary.slice(3, 1) === '1';

        if(binary.slice(4,6) === '00')
            return '7bit';

        if(binary.slice(4,6) === '01')
            return '8bit';

        if(binary.slice(4,6) === '10')
            return '16bit';
    // }
}

pduParser.decode16Bit = function(data, length) {
    //We are getting ucs2 characters.
    var ucs2 = '';
    for(var i = 0;i<=data.length-1;i=i+4) {
        ucs2 += String.fromCharCode("0x"+data[i]+data[i+1]+data[i+2]+data[i+3]);
    }

    return ucs2;
}

pduParser.deSwapNibbles = function(nibbles) {
    var out = '';
    for(var i = 0; i< nibbles.length; i=i+2) {
        if(nibbles[i] === 'F') //Dont consider trailing F.
            out += parseInt(nibbles[i+1], 16).toString(10);
        else
            out += parseInt(nibbles[i+1], 16).toString(10)+parseInt(nibbles[i], 16).toString(10);
    }
    return out;
}

pduParser.decode7Bit = function(code, length, unPadding) {
    //We are getting 'septeps'. We should decode them.
    var binary = '';
    for(var i = 0; i<code.length;i++)
        binary += ('0000'+parseInt(code.slice(i,i+1), 16).toString(2)).slice(-4);

    //This step is for 'unpadding' the padded data. If it has been encoded with 1 bit padding as it
    //happens when the sender used a 7-bit message concatenation (cf http://mobiletidings.com/2009/02/18/combining-sms-messages/)
    if (unPadding){
        var binary2 = '';
        binary = binary + '00000000';
        for (var i=0; i<binary.length/8 - 1 ; i++)
        {
            binary2 += (binary.slice((i+1)*8+(8-unPadding), (i+2)*8) + binary.slice(i*8,i*8+(8-unPadding)));
        }
        binary = binary2;
    }

    var bin = Array();
    var cursor = 0;
    var fromPrevious = '';
    var i = 0;
    while(binary[i]) {
        var remaining = 7 - fromPrevious.length;
        var toNext = 8 - remaining;
        bin[i] = binary.slice(cursor+toNext, cursor+toNext+remaining) + fromPrevious;
        var fromPrevious = binary.slice(cursor, cursor+toNext);
        if(toNext === 8)
            fromPrevious = '';
        else
            cursor += 8;
        i++;
    }

    var ascii = '';
    var esc = false; //last character was a ESC
    for(var i=0; i<length; i++){
        var codeNum = parseInt(bin[i], 2);
        if (codeNum == 0x1B){
            esc = true;
            continue;
        }
        if (esc)
            ascii += sevenBitEsc[codeNum];
        else
            ascii += sevenBitDefault[codeNum];
        esc = false;
    }
    return ascii;
}

pduParser.encode7Bit = function(inTextNumberArray, paddingBits)
{
    //as explained here http://mobiletidings.com/2009/07/06/how-to-pack-gsm7-into-septets/
    var paddingBits = paddingBits || 0;
    var bits = 0;
    var out = "";

    if(paddingBits)
        {
            bits = 7 - paddingBits;
            var octet = (inTextNumberArray[0] << (7 - bits)) % 256
            out += ('00' + octet.toString(16)).slice(-2);
            bits++;
        }

    for(var i = 0; i < inTextNumberArray.length; i++ )
    {
        if( bits == 7 )
        {
            bits = 0;
            continue;
        }
        var octet = (inTextNumberArray[i] & 0x7f) >> bits;
        if( i < inTextNumberArray.length - 1 )
            {octet |= (inTextNumberArray[i + 1] << (7 - bits))%256;}
        out += ('00' + octet.toString(16)).slice(-2);
        bits++;
    }
    return out;
}

pduParser.encode16Bit = function(inTextNumberArray) {
    var out = '';
    for(var i = 0; i<inTextNumberArray.length;i++) {
        out += ('0000'+(inTextNumberArray[i].toString(16))).slice(-4);
    }
    return out;
}

pduParser.messageToNumberArray = function(message) //sp
{
    //7bit GSM encoding according to GSM_03.38 character set http://en.wikipedia.org/wiki/GSM_03.38
    res = [];
    for (var k=0; k<message.text.length; k++)
    {
        if (message.encoding == '7bit'){
            var character = message.text[k];
            for(var i=0;i<sevenBitDefault.length;i++)
            {
                if(sevenBitDefault[i] == character)
                    res.push(i);
                if (sevenBitEsc[i] == character){
                    res.push(0x1B); //escape character
                    res.push(i);
                }
            }
        }
        else if (message.encoding == '16bit')
            res.push(message.text.charCodeAt(k));
    }
    return res;
};

/**
 * Encodes message into PDU format
 * http://www.developershome.com/sms/cmgsCommand4.asp
 * Possible message values:
 *  smsc - SMS center to use (MSISDN) (default:use modem's default SMSC)
 *  smsc_type - SMS center type (0x81 for international and local, 0x91 for international format only) (default:0x81)
 *  receiver - Receiver of the SMS message
 *  receiver_type - type of the receiver's MSISDN (same as smsc_type) (default:0x81)
 *  text - text of the short message
 *  encoding - '7bit' or '16bit' (UCS-2)
 *  request_status - boolean, true to request delivery status
 */
pduParser.generate = function(message) {
    var smsc = '';
    var smscPartLength = 0;

	if (message.smsc!==undefined){
        if (message.smsc_type!==undefined && (message.smsc_type==0x81 || message.smsc_type==0x91)){
            smsc += message.smsc_type.toString (16);
        } else {
            smsc += '81';
        }
        smsc += this.swapNibbles(message.smsc);
        var smsc_length = octetLength(smsc);
        smsc = smsc_length + smsc;
    } else {
        smsc = '00';
    }
    var pdu = smsc;
    smscPartLength = smsc.length;

    var parts = 1;
    var inTextNumberArray = this.messageToNumberArray(message);

    if(message.encoding === '16bit' && inTextNumberArray.length > 70)
        parts = inTextNumberArray.length / 66;

    else if(message.encoding === '7bit' && inTextNumberArray.length > 160)
        parts = inTextNumberArray.length / 153;

    parts = Math.ceil(parts);

    TPMTI  = 1<<0; //(2 bits) type msg, 1=submit by MS
    TPRD   = 1<<2; //(1 bit) reject duplicates
    TPVPF  = 1<<3; //(2 bits) validaty f. : 0=not pres, 1=enhanc,2=relative,3=absolute
    TPSRR  = 1<<5; //(1 bit) want status reply
    TPUDHI = 1<<6; //(1 bit) 1=header+data, 0=only data
    TPRP   = 1<<7; //(1 bit) reply-path

    var submit = TPMTI;

    if(parts > 1) //UDHI
        submit = submit | TPUDHI;

    if (message.request_status!==undefined && message.request_status)
    	submit = submit | TPSRR;
    pdu += ('00'+submit.toString(16)).slice(-2);
    pdu += '00'; //Reference Number;
    var receiverSize = ('00'+(parseInt(message.receiver.length, 10).toString(16))).slice(-2);
    var receiver = pduParser.swapNibbles(message.receiver);

	//Destination MSISDN type
    var receiverType;
    if (message.receiver_type !== undefined && (message.receiver_type === 0x81 || message.receiver_type === 0x91)){
        receiverType = message.receiver_type.toString(16);
    } else {
        receiverType = 81;
    }
    pdu += receiverSize.toString(16) + receiverType + receiver;
    pdu += '00'; //TODO TP-PID

    if(message.encoding === '16bit')
        pdu += '08';
    else if(message.encoding === '7bit')
        pdu += '00';

    var pdus = new Array();

    var csms = randomHexa(2); // CSMS allows to give a reference to a concatenated message

    for(var i=0; i< parts; i++) {
        pdus[i] = pdu;

        if(message.encoding === '16bit') {
            /* If there are more than one messages to be sent, we are going to have to put some UDH. Then, we would have space only
             * for 66 UCS2 characters instead of 70 */
            if(parts === 1)
                var length = 70;
            else
                var length = 66;

        } else if(message.encoding === '7bit') {
            /* If there are more than one messages to be sent, we are going to have to put some UDH. Then, we would have space only
             * for 153 ASCII characters instead of 160 */
            if(parts === 1)
                var length = 160;
            else
                var length = 153;
        } else if(message.encoding === '8bit') {

        }
        var text = inTextNumberArray.slice(i*length, (i*length)+length);

        var user_data;
        if(message.encoding === '16bit') {
            user_data = pduParser.encode16Bit(text);
            var size = (user_data.length / 2);

            if(parts > 1)
                size += 6; //6 is the number of data headers we append.

        } else if(message.encoding === '7bit') {
            if(parts > 1){
                user_data = pduParser.encode7Bit(text,1);
                var size = 7 + text.length;
            }
            else {
                user_data = pduParser.encode7Bit(text);
                var size = text.length;
            }
        }

        pdus[i] += ('00'+parseInt(size).toString(16)).slice(-2);

        // UDHI control header for concaterating message's parts
        if(parts > 1) {
            pdus[i] += '05';
            pdus[i] += '00';
            pdus[i] += '03';
            pdus[i] +=  csms;
            pdus[i] += ('00'+parts.toString(16)).slice(-2);
            pdus[i] += ('00'+(i+1).toString(16)).slice(-2);
        }
        pdus[i] += user_data;
        pdus[i] = {
        	tpdu_length: (pdus[i].length - smscPartLength)/2,
        	smsc_tpdu: pdus[i].toUpperCase()
        };
    }

    return pdus;
}


pduParser.swapNibbles = function(nibbles) {
    var out = '';
    for(var i = 0; i< nibbles.length; i=i+2) {
        if(typeof(nibbles[i+1]) === 'undefined') // Add a trailing F.
            out += 'F'+parseInt(nibbles[i], 16).toString(10);
        else
            out += parseInt(nibbles[i+1], 16).toString(10)+parseInt(nibbles[i], 16).toString(10);
    }
    return out;
}

pduParser.parseStatusReport = function(pdu, smsc_parsed) {
    var cursor = 0;
    var obj = smsc_parsed;

    var header = parseInt(pdu.slice(cursor,cursor+2));
    cursor += 2;
    //TODO: maybe SMS-COMMAND here

    obj.reference = parseInt(pdu.slice(cursor,cursor+2), 16);
    cursor += 2;

    var senderSize = parseInt(pdu.slice(cursor,cursor+2), 16);
    if(senderSize % 2 === 1)
        senderSize++;
    cursor += 2;

    obj.sender_type = parseInt(pdu.slice(cursor,cursor+2));
    cursor += 2;

    obj.sender = pduParser.deSwapNibbles(pdu.slice(cursor, cursor+senderSize));
    cursor += senderSize;

    obj.smsc_ts = parseTS(pdu.slice(cursor, cursor+14));
    cursor += 14;
    obj.discharge_ts = parseTS(pdu.slice(cursor, cursor+14));
    cursor += 14;

    obj.status = pdu.slice(cursor, cursor+2);

    return obj;
}
/**
 * Parses SMSC part of the PDU
 */
function parseSMSCPart (pdu){
	//Cursor points to the last octet we've read.
    var cursor = 0;

    var buffer = new Buffer(pdu.slice(0,4), 'hex');
    var smscSize = buffer[0];
    var smscType = buffer[1].toString(16);
    var smscNum  = pduParser.deSwapNibbles(pdu.slice(4, smscSize*2+2));
    return {
    	'smsc' : smscNum,
    	'smsc_type' : smscType,
    	'length' : smscSize*2+2
    };
}
/**
 * Parses timestamp from PDU
 */
function parseTS (ts){
	var t = pduParser.deSwapNibbles (ts);

	var time = new Date;
    time.setFullYear(2000+parseInt (t.substr (0,2)));
    time.setMonth(parseInt (t.substr(2,2))-1);
    time.setDate(parseInt (t.substr(4,2)));
    time.setHours(parseInt (t.substr(6,2)));
    time.setMinutes(parseInt (t.substr(8,2)));
    time.setSeconds(parseInt (t.substr(10,2)));

    var firstTimezoneOctet = parseInt(t.substr(12,1));
    var binary = ("0000"+firstTimezoneOctet.toString(2)).slice(-4);
    var factor = binary.slice(0,1) === '1' ? 1 : -1;
    var binary = '0'+binary.slice(1, 4);
    var firstTimezoneOctet = parseInt(binary, 2).toString(10);
    var timezoneDiff = parseInt(firstTimezoneOctet + t.substr(13, 1));
    var time = new Date(time.getTime() + (timezoneDiff * 15 * 60000 * factor) - time.getTimezoneOffset()*60000);

	return time;
}

function TP_MTI_To_String (tp_mti){
	switch (tp_mti){
		case '00': return 'SMS-DELIVER';
		case '01': return 'SMS-SUBMIT';
		case '10': return 'SMS-STATUS-REPORT';
		default: return 'unknown';
	}
}

function randomHexa(size)
{
    var text = "";
    var possible = "0123456789ABCDEF";
    for( var i=0; i < size; i++ )
        text += possible.charAt(Math.floor(Math.random() * possible.length));
    return text;
}

/**
 * Return length of the octet
 */
function octetLength (str) {
    var len = (str.toString().length/2).toString(16).toUpperCase();
    if (len.length==1) len = '0' + len;
    return len;
};

/**
 * Encodes ussd request to PDU
 */
pduParser.ussdEncode = function (ussd) {
	var arr = this.messageToNumberArray ({text:ussd,encoding:'7bit'});
	return this.encode7Bit(arr).toUpperCase();
};

module.exports = pduParser;
